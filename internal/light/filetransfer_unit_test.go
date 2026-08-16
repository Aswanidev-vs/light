package light

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// setHome points the on-disk config/history writes at a temp dir so tests that
// exercise TransferManager finalize() don't touch the real user profile.
func setHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/b", "b"}, // directories are stripped via filepath.Base
		{"foo/bar.txt", "bar.txt"},
		{"/etc/passwd", "passwd"},
		{"plain.txt", "plain.txt"},
		{"a/b/c", "c"},
		{"..", ".."},
		{"", "."},
	}
	for _, c := range cases {
		if got := sanitize(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	if got := uniquePath(p); got != p {
		t.Fatalf("uniquePath on absent file = %q, want %q", got, p)
	}
	if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	want1 := filepath.Join(dir, "x (1).txt")
	if got := uniquePath(p); got != want1 {
		t.Fatalf("uniquePath collision = %q, want %q", got, want1)
	}
	if err := os.WriteFile(want1, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uniquePath(p); got != filepath.Join(dir, "x (2).txt") {
		t.Fatalf("uniquePath second collision = %q, want %q", got, filepath.Join(dir, "x (2).txt"))
	}
}

func TestAtoi64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"  ", 0},
		{"42", 42},
		{"  -7 ", -7},
		{"abc", 0},
		{"999999999999", 999999999999},
	}
	for _, c := range cases {
		if got := atoi64(c.in); got != c.want {
			t.Errorf("atoi64(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestObservedPeerAddress(t *testing.T) {
	withRemote := func(addr string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/prepare", nil)
		r.RemoteAddr = addr
		return r
	}

	if got := observedPeerAddress(withRemote("192.168.1.5:1234"), "10.0.0.1:9120", 9120); got != "192.168.1.5:9120" {
		t.Errorf("observed IPv4 = %q, want 192.168.1.5:9120", got)
	}
	if got := observedPeerAddress(withRemote("not-an-addr"), "1.2.3.4:9120", 9120); got != "1.2.3.4:9120" {
		t.Errorf("fallback = %q, want 1.2.3.4:9120", got)
	}
	// Observed host wins; advertised port is reused when it carries one.
	if got := observedPeerAddress(withRemote("192.168.1.5:1234"), "10.0.0.9:9999", 9120); got != "192.168.1.5:9999" {
		t.Errorf("advertised port reuse = %q, want 192.168.1.5:9999", got)
	}
}

func TestNormalizeTransportMode(t *testing.T) {
	cases := map[string]string{
		"quic":   "quic",
		"QUIC":   "quic",
		" quic ": "quic",
		"tcp":    "tcp",
		"":       "tcp",
		"banana": "tcp",
		"TCP":    "tcp",
	}
	for in, want := range cases {
		if got := normalizeTransportMode(in); got != want {
			t.Errorf("normalizeTransportMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParallelUploadLimitClamps(t *testing.T) {
	service := NewFileTransferService(nil, &TransferManager{active: make(map[string]*Transfer)}, &SettingsService{}, nil)
	service.deviceType = DeviceTypeDesktop
	if got := service.parallelUploadLimit(); got != maxParallelUploads {
		t.Errorf("desktop limit = %d, want %d", got, maxParallelUploads)
	}
	service.deviceType = DeviceTypeMobile
	if got := service.parallelUploadLimit(); got != mobileParallelUploads {
		t.Errorf("mobile limit = %d, want %d", got, mobileParallelUploads)
	}
	service.deviceType = ""
	if got := service.parallelUploadLimit(); got < 1 {
		t.Errorf("default limit = %d, want at least 1", got)
	}
}

func TestLightQUICConfig(t *testing.T) {
	cfg := lightQUICConfig()
	if cfg == nil {
		t.Fatal("lightQUICConfig returned nil")
	}
	if cfg.MaxIncomingStreams <= 0 {
		t.Error("MaxIncomingStreams should be positive")
	}
	if cfg.InitialStreamReceiveWindow == 0 || cfg.MaxStreamReceiveWindow == 0 {
		t.Error("stream receive windows should be non-zero")
	}
	if cfg.MaxIdleTimeout <= 0 {
		t.Error("MaxIdleTimeout should be positive")
	}
	if !cfg.Allow0RTT {
		t.Error("Allow0RTT should be enabled for known peers")
	}
}

// TestCountingReaderHonorsPauseAndCancel verifies the upload hot path blocks
// while paused and resumes / cancels without leaking the reader goroutine.
func TestCountingReaderHonorsPauseAndCancel(t *testing.T) {
	ctrl := &sendControl{status: StatusActive, resumeCh: make(chan struct{})}
	atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusActive)

	data := bytes.Repeat([]byte("x"), 100)
	cr := &countingReader{
		r:        bytes.NewReader(data),
		ctrl:     ctrl,
		subID:    "s",
		fname:    "f",
		size:     100,
		started:  time.Now(),
		lastEmit: time.Now(),
		manager:  &TransferManager{active: make(map[string]*Transfer)},
	}

	buf := make([]byte, 10)
	if n, err := cr.Read(buf); n != 10 || err != nil {
		t.Fatalf("active read = %d/%v, want 10/nil", n, err)
	}

	ctrl.mu.Lock()
	ctrl.status = StatusPaused
	atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusPaused)
	ctrl.mu.Unlock()

	readDone := make(chan int, 1)
	go func() {
		n, _ := cr.Read(buf)
		readDone <- n
	}()
	select {
	case n := <-readDone:
		t.Fatalf("Read returned %d while paused", n)
	case <-time.After(100 * time.Millisecond):
	}

	resumeControl(ctrl)
	select {
	case n := <-readDone:
		if n <= 0 {
			t.Fatalf("Read after resume returned %d, want > 0", n)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not resume after 1s")
	}

	atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusCancelled)
	if n, err := cr.Read(buf); n != 0 || err != context.Canceled {
		t.Fatalf("cancelled Read = %d/%v, want 0/context.Canceled", n, err)
	}
}

// resumeControl mirrors the production ResumeTransfer channel swap so the test
// can wake a blocked reader without touching the manager/event plumbing.
func resumeControl(ctrl *sendControl) {
	ctrl.mu.Lock()
	ctrl.status = StatusActive
	atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusActive)
	old := ctrl.resumeCh
	ctrl.resumeCh = make(chan struct{})
	ctrl.mu.Unlock()
	close(old)
}

// TestRateTrackerReportsCurrentNotLifetimeAverage pins the speed regression: the
// reporter must track the bytes moved in the RECENT window, not total/elapsed.
// After a fast window following a slow start the smoothed rate must EXCEED the
// lifetime average (which can only ever fall), proving the new behaviour.
func TestRateTrackerReportsCurrentNotLifetimeAverage(t *testing.T) {
	started := time.Now()
	rt := rateTracker{started: started}

	// Window 1: 100 bytes over 1s => 100 B/s, primes the tracker.
	r1 := rt.sample(started.Add(1*time.Second), 100)
	if r1 != 100 {
		t.Fatalf("primed rate = %d, want 100", r1)
	}

	// Window 2: another 100 bytes in just 0.5s => instant 200 B/s.
	r2 := rt.sample(started.Add(1500*time.Millisecond), 200)
	elapsed := 1.5                               // runtime value: keeps the division out of constant folding
	lifetimeAvg := int64(float64(200) / elapsed) // ~133 B/s, the old formula's answer
	if r2 <= lifetimeAvg {
		t.Fatalf("current rate %d should exceed lifetime average %d after a burst", r2, lifetimeAvg)
	}
}

// TestCancellationReaderAbortsWhenFlagged verifies the inbound reader stops the
// copy within one chunk of the cancel predicate flipping.
func TestCancellationReaderAbortsWhenFlagged(t *testing.T) {
	flag := false
	cr := &cancellationReader{
		r:         bytes.NewReader(bytes.Repeat([]byte("z"), 64)),
		cancelled: func() bool { return flag },
	}
	buf := make([]byte, 8)
	if n, err := cr.Read(buf); n != 8 || err != nil {
		t.Fatalf("read before cancel = %d/%v, want 8/nil", n, err)
	}
	flag = true
	if n, err := cr.Read(buf); n != 0 || !errors.Is(err, errTransferCancelled) {
		t.Fatalf("read after cancel = %d/%v, want 0/errTransferCancelled", n, err)
	}
}
