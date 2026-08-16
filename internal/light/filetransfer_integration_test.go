package light

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileTransferHTTPIntegration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	downloadDir := t.TempDir()
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{
		DownloadDir: downloadDir,
		AutoAccept:  true,
	}}
	service := NewFileTransferService(nil, manager, settings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", service.handlePrepare)
	mux.HandleFunc("/api/transfer", service.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	transferID := "integration-transfer"
	filename := "Bleach ghost phonk edit [final].mkv"
	payload := []byte("integration transfer payload")
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])

	prepareBody, err := json.Marshal(PreparePayload{
		TransferID: transferID,
		SenderID:   "integration-sender-id",
		SenderName: "integration sender",
		SenderAddr: "192.168.1.10:9120",
		SenderType: DeviceTypeDesktop,
		Files:      []FileManifestEntry{{Name: filename, Size: int64(len(payload))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse, err := http.Post(server.URL+"/api/prepare", "application/json", bytes.NewReader(prepareBody))
	if err != nil {
		t.Fatal(err)
	}
	defer prepareResponse.Body.Close()
	if prepareResponse.StatusCode != http.StatusOK {
		t.Fatalf("prepare status = %d, want %d", prepareResponse.StatusCode, http.StatusOK)
	}
	state := service.accepts[transferID]
	if state == nil || state.senderID != "integration-sender-id" || state.senderAddr != "127.0.0.1:9120" || state.senderType != DeviceTypeDesktop {
		t.Fatalf("sender metadata = %#v, want observed sender identity", state)
	}

	transferRequest, err := http.NewRequest(http.MethodPut, server.URL+"/api/transfer", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	transferRequest.Header.Set("X-Transfer-Id", transferID)
	transferRequest.Header.Set("X-Filename", filename)
	transferRequest.Header.Set("X-File-Size", strconv.FormatInt(int64(len(payload)), 10))
	transferResponse, err := http.DefaultClient.Do(transferRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer transferResponse.Body.Close()
	if transferResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(transferResponse.Body)
		t.Fatalf("transfer status = %d, want %d: %s", transferResponse.StatusCode, http.StatusOK, strings.TrimSpace(string(body)))
	}
	responseBody, err := io.ReadAll(transferResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(responseBody)); got != "ok "+checksum {
		t.Fatalf("transfer response = %q, want %q", got, "ok "+checksum)
	}

	stored, err := os.ReadFile(filepath.Join(downloadDir, filename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payload) {
		t.Fatalf("stored payload = %q, want %q", stored, payload)
	}
	history := manager.GetHistory(1)
	if len(history) != 1 || history[0].Status != StatusCompleted || history[0].Size != int64(len(payload)) || history[0].FilePath == "" {
		t.Fatalf("history = %#v, want one completed transfer with a file path", history)
	}
}

func TestPrepareUsesObservedPeerAddressForReverseSharing(t *testing.T) {
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{Port: 9120, AutoAccept: true}}
	service := NewFileTransferService(nil, manager, settings, nil)

	payload, err := json.Marshal(PreparePayload{
		TransferID: "reverse-share",
		SenderAddr: "169.254.195.132:9120",
		Files:      []FileManifestEntry{{Name: "file.bin", Size: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/prepare", bytes.NewReader(payload))
	request.RemoteAddr = "192.168.1.50:54321"
	response := httptest.NewRecorder()

	service.handlePrepare(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, want %d", response.Code, http.StatusOK)
	}
	state := service.accepts["reverse-share"]
	if state == nil || state.senderAddr != "192.168.1.50:9120" {
		t.Fatalf("sender address = %#v, want observed LAN address", state)
	}
}

func TestIsUsableLANIPv4RejectsLinkLocalAddresses(t *testing.T) {
	if isUsableLANIPv4(net.ParseIP("169.254.195.132")) {
		t.Fatal("link-local address was accepted as a LAN endpoint")
	}
	if !isUsableLANIPv4(net.ParseIP("192.168.1.50")) {
		t.Fatal("private LAN address was rejected")
	}
}

func TestSendFilesUploadsInParallel(t *testing.T) {
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DeviceName: "parallel sender", Port: 9120}, id: "parallel-sender-id"}
	service := NewFileTransferService(nil, manager, settings, nil)

	var active int32
	var peak int32
	updatePeak := func(value int32) {
		for {
			old := atomic.LoadInt32(&peak)
			if value <= old || atomic.CompareAndSwapInt32(&peak, old, value) {
				return
			}
		}
	}

	var manifest PreparePayload
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("accepted"))
	})
	mux.HandleFunc("/api/transfer", func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&active, 1)
		updatePeak(current)
		defer atomic.AddInt32(&active, -1)

		time.Sleep(50 * time.Millisecond)
		if err := writeTransferReceipt(w, r); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	paths := make([]string, 8)
	for i := range paths {
		paths[i] = filepath.Join(t.TempDir(), "file-"+strconv.Itoa(i)+".bin")
		if err := os.WriteFile(paths[i], []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	err := service.SendFiles(TransferRequest{DeviceAddr: server.Listener.Addr().String(), FilePaths: paths})
	if err != nil {
		t.Fatalf("SendFiles() error = %v", err)
	}
	if peak < 2 {
		t.Fatalf("peak parallel uploads = %d, want more than one", peak)
	}
	if peak > maxParallelUploads {
		t.Fatalf("peak parallel uploads = %d, want at most %d", peak, maxParallelUploads)
	}
	if manifest.SenderID != "parallel-sender-id" || manifest.SenderAddr == "" || manifest.SenderType != DeviceTypeDesktop {
		t.Fatalf("manifest sender metadata = %#v, want sender identity, address, and type", manifest)
	}
}

func TestMobileParallelUploadLimit(t *testing.T) {
	if mobileParallelUploads >= maxParallelUploads {
		t.Fatalf("mobile parallel uploads = %d, want strictly less than desktop limit %d", mobileParallelUploads, maxParallelUploads)
	}

	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{Port: 9120, AutoAccept: true}}
	service := NewFileTransferService(nil, manager, settings, nil)

	service.deviceType = DeviceTypeDesktop
	if got := service.parallelUploadLimit(); got != maxParallelUploads {
		t.Fatalf("desktop limit = %d, want %d", got, maxParallelUploads)
	}
	service.deviceType = DeviceTypeMobile
	if got := service.parallelUploadLimit(); got != mobileParallelUploads {
		t.Fatalf("mobile limit = %d, want %d", got, mobileParallelUploads)
	}
}

func TestSendFilesRespectsMobileParallelLimit(t *testing.T) {
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DeviceName: "mobile sender", Port: 9121}, id: "mobile-sender-id"}
	service := NewFileTransferService(nil, manager, settings, nil)
	service.deviceType = DeviceTypeMobile

	var active int32
	var peak int32
	updatePeak := func(value int32) {
		for {
			old := atomic.LoadInt32(&peak)
			if value <= old || atomic.CompareAndSwapInt32(&peak, old, value) {
				return
			}
		}
	}

	var received int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("accepted"))
	})
	mux.HandleFunc("/api/transfer", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&received, 1)
		current := atomic.AddInt32(&active, 1)
		updatePeak(current)
		defer atomic.AddInt32(&active, -1)

		time.Sleep(50 * time.Millisecond)
		if err := writeTransferReceipt(w, r); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	paths := make([]string, 8)
	for i := range paths {
		paths[i] = filepath.Join(t.TempDir(), "mfile-"+strconv.Itoa(i)+".bin")
		if err := os.WriteFile(paths[i], []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.SendFiles(TransferRequest{DeviceAddr: server.Listener.Addr().String(), FilePaths: paths}); err != nil {
		t.Fatalf("SendFiles() error = %v", err)
	}
	if int(peak) > mobileParallelUploads {
		t.Fatalf("mobile peak parallel uploads = %d, want at most %d", peak, mobileParallelUploads)
	}
	if peak < 1 {
		t.Fatalf("mobile peak parallel uploads = %d, want at least one", peak)
	}
	if int(received) != len(paths) {
		t.Fatalf("received %d files, want %d", received, len(paths))
	}
}

func TestSendFilesContinuesAfterPerFileFailure(t *testing.T) {
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DeviceName: "failure sender", Port: 9120}, id: "failure-sender-id"}
	service := NewFileTransferService(nil, manager, settings, nil)

	var completed int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("accepted"))
	})
	mux.HandleFunc("/api/transfer", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Filename") == "file-0.bin" {
			_, _ = io.Copy(io.Discard, r.Body)
			http.Error(w, "intentional test failure", http.StatusInternalServerError)
			return
		}
		atomic.AddInt32(&completed, 1)
		if err := writeTransferReceipt(w, r); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	paths := make([]string, 3)
	for i := range paths {
		paths[i] = filepath.Join(t.TempDir(), "file-"+strconv.Itoa(i)+".bin")
		if err := os.WriteFile(paths[i], []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	err := service.SendFiles(TransferRequest{DeviceAddr: server.Listener.Addr().String(), FilePaths: paths})
	if err == nil || !strings.Contains(err.Error(), "file-0.bin") {
		t.Fatalf("SendFiles() error = %v, want the failed file reported", err)
	}
	if got := atomic.LoadInt32(&completed); got != 2 {
		t.Fatalf("successful sibling uploads = %d, want 2", got)
	}
}

func writeTransferReceipt(w http.ResponseWriter, r *http.Request) error {
	h := sha256.New()
	if _, err := io.Copy(h, r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return err
	}
	w.WriteHeader(http.StatusOK)
	_, err := fmt.Fprintf(w, "ok %s", hex.EncodeToString(h.Sum(nil)))
	return err
}

func TestSendFilesRejectsReceiverChecksumMismatch(t *testing.T) {
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DeviceName: "checksum sender", Port: 9120}, id: "checksum-sender-id"}
	service := NewFileTransferService(nil, manager, settings, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("accepted"))
	})
	mux.HandleFunc("/api/transfer", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ok %s", strings.Repeat("0", sha256.Size*2))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "checksum.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.SendFiles(TransferRequest{DeviceAddr: server.Listener.Addr().String(), FilePaths: []string{path}}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("SendFiles() error = %v, want checksum mismatch", err)
	}
}

func TestFileTransferHTTPIntegrationRejectsChecksumMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	downloadDir := t.TempDir()
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	service := NewFileTransferService(nil, manager, settings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", service.handlePrepare)
	mux.HandleFunc("/api/transfer", service.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	transferID := "checksum-mismatch"
	filename := "broken.bin"
	payload := []byte("payload")
	prepareBody, err := json.Marshal(PreparePayload{
		TransferID: transferID,
		Files:      []FileManifestEntry{{Name: filename, Size: int64(len(payload)), Checksum: strings.Repeat("0", 64)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse, err := http.Post(server.URL+"/api/prepare", "application/json", bytes.NewReader(prepareBody))
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse.Body.Close()

	request, err := http.NewRequest(http.MethodPut, server.URL+"/api/transfer", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Transfer-Id", transferID)
	request.Header.Set("X-Filename", filename)
	request.Header.Set("X-File-Size", strconv.FormatInt(int64(len(payload)), 10))
	request.Header.Set("X-Checksum-Sha256", strings.Repeat("0", 64))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("checksum status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	history := manager.GetHistory(1)
	if len(history) != 1 || history[0].Status != StatusFailed || history[0].Error != "checksum mismatch" {
		t.Fatalf("history = %#v, want checksum failure", history)
	}
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("download directory contains failed-transfer artifacts: %#v", entries)
	}
}

func TestFileTransferHTTPIntegrationCleansReadFailure(t *testing.T) {
	downloadDir := t.TempDir()
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	service := NewFileTransferService(nil, manager, settings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", service.handlePrepare)
	mux.HandleFunc("/api/transfer", service.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	transferID := "read-failure"
	filename := "partial.bin"
	prepareBody, err := json.Marshal(PreparePayload{
		TransferID: transferID,
		Files:      []FileManifestEntry{{Name: filename, Size: 20, Checksum: strings.Repeat("0", 64)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse, err := http.Post(server.URL+"/api/prepare", "application/json", bytes.NewReader(prepareBody))
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse.Body.Close()

	request := httptest.NewRequest(http.MethodPut, "/api/transfer", failingBody{})
	request.Header.Set("X-Transfer-Id", transferID)
	request.Header.Set("X-Filename", filename)
	request.Header.Set("X-File-Size", "20")
	request.Header.Set("X-Checksum-Sha256", strings.Repeat("0", 64))
	response := httptest.NewRecorder()
	service.handleTransfer(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("read failure status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("download directory contains read-failure artifacts: %#v", entries)
	}
}

type failingBody struct{}

func (failingBody) Read(p []byte) (int, error) {
	n := copy(p, []byte("partial"))
	return n, io.ErrUnexpectedEOF
}

func (failingBody) Close() error { return nil }

func TestCleanupPartialFilesRemovesOnlyStaleArtifacts(t *testing.T) {
	dir := t.TempDir()
	oldPartial := filepath.Join(dir, "file.bin.light-partial-old")
	freshPartial := filepath.Join(dir, "file.bin.light-partial-fresh")
	unrelated := filepath.Join(dir, "file.bin")
	for _, path := range []string{oldPartial, freshPartial, unrelated} {
		if err := os.WriteFile(path, []byte("cache"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-partialFileMaxAge - time.Hour)
	if err := os.Chtimes(oldPartial, old, old); err != nil {
		t.Fatal(err)
	}

	cleanupPartialFiles(dir)
	if _, err := os.Stat(oldPartial); !os.IsNotExist(err) {
		t.Fatalf("old partial file still exists, stat error = %v", err)
	}
	for _, path := range []string{freshPartial, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %q to remain: %v", path, err)
		}
	}
}

// TestCancelInboundTransferAbortsMidStream pins the "cancel on the receiving
// phone" bug: cancelling an inbound transfer must abort the copy, leave no
// partial artifact, and mark the row cancelled (not failed/completed).
func TestCancelInboundTransferAbortsMidStream(t *testing.T) {
	downloadDir := t.TempDir()
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	service := NewFileTransferService(nil, manager, settings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", service.handlePrepare)
	mux.HandleFunc("/api/transfer", service.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	tid := "cancel-inbound"
	fname := "slow.bin"
	const size = 1 << 20 // 1 MiB so the copy takes several chunks

	prepareBody, _ := json.Marshal(PreparePayload{
		TransferID: tid,
		Files:      []FileManifestEntry{{Name: fname, Size: size}},
	})
	resp, err := http.Post(server.URL+"/api/prepare", "application/json", bytes.NewReader(prepareBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// A slow body: the server reads in 1 MiB chunks while we cancel mid-way.
	body := &slowBody{remaining: size, perChunk: 8 << 10, delay: time.Millisecond}
	req, err := http.NewRequest(http.MethodPut, server.URL+"/api/transfer", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Transfer-Id", tid)
	req.Header.Set("X-Filename", fname)
	req.Header.Set("X-File-Size", strconv.FormatInt(size, 10))

	// Cancel once a few chunks have been delivered.
	go func() {
		time.Sleep(5 * time.Millisecond)
		service.CancelTransfer(tid + ":" + fname)
	}()

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 499 {
		t.Fatalf("cancelled transfer status = %d (%s), want 499", resp.StatusCode, strings.TrimSpace(string(body2)))
	}

	// History row must be cancelled, and no partial file may linger.
	history := manager.GetHistory(1)
	if len(history) == 0 || history[0].Status != StatusCancelled {
		t.Fatalf("history = %#v, want a cancelled transfer", history)
	}
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("download dir after cancel contains artifacts: %#v", entries)
	}
}

// slowBody feeds the transfer endpoint a little data at a time so a cancel can
// land mid-stream in tests without huge payloads.
type slowBody struct {
	remaining int64
	perChunk  int64
	delay     time.Duration
}

func (b *slowBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	n := b.perChunk
	if n > int64(len(p)) {
		n = int64(len(p))
	}
	if n > b.remaining {
		n = b.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'x'
	}
	b.remaining -= n
	return int(n), nil
}

func (b *slowBody) Close() error { return nil }

// TestSendFilesSurfacesReceiverCancelAsCancelled verifies the sender side: when
// the peer reports "cancelled" on /api/status, the upload finalizes as
// cancelled (not a generic network failure) so the sender UI matches the phone.
func TestSendFilesSurfacesReceiverCancelAsCancelled(t *testing.T) {
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DeviceName: "cancel sender", Port: 9120}, id: "cancel-sender-id"}
	service := NewFileTransferService(nil, manager, settings, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("accepted"))
	})
	// Reject the body and answer cancellation on the status probe.
	mux.HandleFunc("/api/transfer", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cancelled", 499)
	})
	mux.HandleFunc("/api/status/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("cancelled"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "cancel-me.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := service.SendFiles(TransferRequest{DeviceAddr: server.Listener.Addr().String(), FilePaths: []string{path}})
	if err != nil {
		// SendFiles aggregates per-file errors; the individual row is what
		// matters — it must be cancelled, not failed.
		if strings.Contains(err.Error(), "checksum") || strings.Contains(err.Error(), "receiver error") {
			t.Fatalf("SendFiles error = %v, want a clean cancellation", err)
		}
	}
	// The row keyed "<tid>:cancel-me.bin" must be cancelled in history.
	for _, trow := range manager.GetHistory(10) {
		if strings.HasSuffix(trow.ID, ":cancel-me.bin") {
			if trow.Status != StatusCancelled {
				t.Fatalf("cancelled upload row status = %q, want %q", trow.Status, StatusCancelled)
			}
			return
		}
	}
	t.Fatalf("no history row found for cancelled upload; history = %#v", manager.GetHistory(10))
}
