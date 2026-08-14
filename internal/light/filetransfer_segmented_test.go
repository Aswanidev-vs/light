package light

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func setSegmentHeaders(req *http.Request, tid, fname string, size, offset int64, index, count int) {
	req.Header.Set("X-Transfer-Id", tid)
	req.Header.Set("X-Filename", fname)
	req.Header.Set("X-File-Size", strconv.FormatInt(size, 10))
	req.Header.Set("X-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("X-Segment-Index", strconv.FormatInt(int64(index), 10))
	req.Header.Set("X-Segment-Count", strconv.FormatInt(int64(count), 10))
}

// TestUploadWithClientSegmentedReassembly drives the full segmented sender path
// (parallel ranges + background hash + reassembly) against a real TCP receiver.
func TestUploadWithClientSegmentedReassembly(t *testing.T) {
	setHome(t)
	downloadDir := t.TempDir()
	defer os.RemoveAll(downloadDir)
	receiverManager := &TransferManager{active: make(map[string]*Transfer)}
	receiverSettings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, receiverManager, receiverSettings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", receiver.handlePrepare)
	mux.HandleFunc("/api/transfer", receiver.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	senderManager := &TransferManager{active: make(map[string]*Transfer)}
	sender := NewFileTransferService(nil, senderManager, &SettingsService{}, nil)
	sender.deviceType = DeviceTypeDesktop // 4 parallel segments

	size := int64(segmentMinSize) + int64(12345)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	srcPath := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	tid := "seg-e2e"
	fname := "big.bin"
	prepareBody, _ := json.Marshal(PreparePayload{TransferID: tid, Files: []FileManifestEntry{{Name: fname, Size: size}}})
	resp, err := http.Post(server.URL+"/api/prepare", "application/json", bytes.NewReader(prepareBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	client := newTCPClient()
	if err := sender.uploadWithClient(tid, server.Listener.Addr().String(), srcPath, fname, size, "", client, "http"); err != nil {
		t.Fatalf("uploadWithClient segmented: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(downloadDir, fname))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("reassembled length %d, want %d", len(got), len(payload))
	}

	history := senderManager.GetHistory(1)
	if len(history) != 1 || history[0].Status != StatusCompleted || history[0].Checksum == "" {
		t.Fatalf("sender history = %#v, want one completed transfer with a checksum", history)
	}
}

// TestHandleSegmentedTransferConcurrentReassembly fires all segments of one file
// concurrently at the receiver to exercise the shared assemblyState mutex under
// the race detector.
func TestHandleSegmentedTransferConcurrentReassembly(t *testing.T) {
	setHome(t)
	downloadDir := t.TempDir()
	defer os.RemoveAll(downloadDir)
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, manager, settings, nil)
	receiver.accepts["seg-conc"] = &acceptState{status: "accepted"}

	const segs = 4
	segSize := 1 << 20
	total := int64(segSize * segs)
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i)
	}
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])

	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := make([]int, segs)
	for i := 0; i < segs; i++ {
		i := i
		start := int64(i) * int64(segSize)
		part := payload[start : start+int64(segSize)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPut, "/api/transfer", bytes.NewReader(part))
			setSegmentHeaders(req, "seg-conc", "big.bin", total, start, i, segs)
			req.Header.Set("X-Checksum-Sha256", checksum)
			w := httptest.NewRecorder()
			receiver.handleTransfer(w, req)
			mu.Lock()
			codes[i] = w.Code
			mu.Unlock()
		}()
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("segment %d status = %d, want 200", i, c)
		}
	}
	got, err := os.ReadFile(filepath.Join(downloadDir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("concurrent reassembly mismatch (%d bytes)", len(got))
	}
}

// TestHandleSegmentedTransferEmitsProgress is a regression test: the segmented
// (parallel-range) receiver path must emit live "transfer-progress" events so
// the receiver UI shows the file being transferred, not just a final
// "transfer-complete". Previously the bytes were copied straight to disk with
// no progress emission, leaving the receiver blind until completion.
func TestHandleSegmentedTransferEmitsProgress(t *testing.T) {
	setHome(t)
	downloadDir := t.TempDir()
	defer os.RemoveAll(downloadDir)
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, manager, settings, nil)

	var mu sync.Mutex
	var events []map[string]any
	receiver.testEmit = func(name string, payload any) {
		if name != "transfer-progress" {
			return
		}
		mu.Lock()
		events = append(events, payload.(map[string]any))
		mu.Unlock()
	}

	receiver.accepts["seg-prog"] = &acceptState{status: "accepted"}

	const segs = 4
	segSize := 1 << 20
	total := int64(segSize * segs)
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i)
	}
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])

	var wg sync.WaitGroup
	for i := 0; i < segs; i++ {
		i := i
		start := int64(i) * int64(segSize)
		part := payload[start : start+int64(segSize)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPut, "/api/transfer", bytes.NewReader(part))
			setSegmentHeaders(req, "seg-prog", "big.bin", total, start, i, segs)
			req.Header.Set("X-Checksum-Sha256", checksum)
			w := httptest.NewRecorder()
			receiver.handleTransfer(w, req)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected transfer-progress events on the receiver, got none")
	}
	last := int64(0)
	for _, e := range events {
		if e["id"] != "seg-prog:big.bin" {
			t.Fatalf("unexpected event id %v", e["id"])
		}
		if e["size"] != total {
			t.Fatalf("event size = %v, want %d", e["size"], total)
		}
		tr := e["transferred"].(int64)
		if tr < last {
			t.Fatalf("transferred went backwards: %d < %d", tr, last)
		}
		last = tr
	}
	if last != total {
		t.Fatalf("final transferred = %d, want %d", last, total)
	}
}

// TestHandleSegmentedTransferChecksumMismatch verifies that a wrong declared
// checksum fails the final reassembly and leaves no partial artifact behind.
func TestHandleSegmentedTransferChecksumMismatch(t *testing.T) {
	setHome(t)
	downloadDir := t.TempDir()
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, manager, settings, nil)
	receiver.accepts["seg-mm"] = &acceptState{status: "accepted"}

	partA := bytes.Repeat([]byte("A"), 1<<20)
	partB := bytes.Repeat([]byte("B"), 1<<20)
	total := int64(len(partA) + len(partB))

	req0 := httptest.NewRequest(http.MethodPut, "/api/transfer", bytes.NewReader(partA))
	setSegmentHeaders(req0, "seg-mm", "file.bin", total, 0, 0, 2)
	// The first segment to arrive fixes the declared checksum for the whole
	// reassembly; a wrong value must fail the final verify.
	req0.Header.Set("X-Checksum-Sha256", strings.Repeat("0", 64))
	w0 := httptest.NewRecorder()
	receiver.handleTransfer(w0, req0)
	if w0.Code != http.StatusOK {
		t.Fatalf("segment 0 status = %d, want 200", w0.Code)
	}

	req1 := httptest.NewRequest(http.MethodPut, "/api/transfer", bytes.NewReader(partB))
	setSegmentHeaders(req1, "seg-mm", "file.bin", total, int64(len(partA)), 1, 2)
	req1.Header.Set("X-Checksum-Sha256", strings.Repeat("0", 64))
	w1 := httptest.NewRecorder()
	receiver.handleTransfer(w1, req1)
	if w1.Code != http.StatusBadRequest {
		t.Fatalf("last segment status = %d, want 400 (checksum mismatch)", w1.Code)
	}

	entries, _ := os.ReadDir(downloadDir)
	if len(entries) != 0 {
		t.Fatalf("partial not cleaned up: %v", entries)
	}
	history := manager.GetHistory(1)
	if len(history) != 1 || history[0].Status != StatusFailed {
		t.Fatalf("history = %#v, want one failed transfer", history)
	}
}

// TestHandleTransferTruncatesStalePartial confirms a fresh single-stream
// transfer overwrites any leftover partial instead of appending to it.
func TestHandleTransferTruncatesStalePartial(t *testing.T) {
	setHome(t)
	downloadDir := t.TempDir()
	defer os.RemoveAll(downloadDir)
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, manager, settings, nil)
	receiver.accepts["fresh"] = &acceptState{status: "accepted"}

	fname := "fresh.bin"
	finalPath := filepath.Join(downloadDir, fname)
	partialPath := finalPath + ".light-partial-fresh"
	// Simulate a leftover partial from a previous interrupted attempt.
	if err := os.WriteFile(partialPath, []byte("STALESTALESTALE"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := []byte("GOOD")
	sum := sha256.Sum256(body)
	req := httptest.NewRequest(http.MethodPut, "/api/transfer", bytes.NewReader(body))
	req.Header.Set("X-Transfer-Id", "fresh")
	req.Header.Set("X-Filename", fname)
	req.Header.Set("X-File-Size", strconv.FormatInt(int64(len(body)), 10))
	req.Header.Set("X-Checksum-Sha256", hex.EncodeToString(sum[:]))
	w := httptest.NewRecorder()
	receiver.handleTransfer(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("fresh transfer status = %d, body=%s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("result = %q, want %q (stale bytes were not truncated)", got, body)
	}
}

// TestSendFilesEndToEndTCP exercises the full send pipeline (prepare + parallel
// uploads, mixing single-stream small files and a segmented large file) over a
// real TCP receiver.
func TestSendFilesEndToEndTCP(t *testing.T) {
	setHome(t)
	downloadDir := t.TempDir()
	defer os.RemoveAll(downloadDir)
	receiverManager := &TransferManager{active: make(map[string]*Transfer)}
	receiverSettings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, receiverManager, receiverSettings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", receiver.handlePrepare)
	mux.HandleFunc("/api/transfer", receiver.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	senderManager := &TransferManager{active: make(map[string]*Transfer)}
	sender := NewFileTransferService(nil, senderManager, &SettingsService{}, nil)

	smallA := []byte("small a payload")
	smallB := []byte("another small payload here")
	bigSize := int64(segmentMinSize) + int64(999)
	big := make([]byte, bigSize)
	for i := range big {
		big[i] = byte(i)
	}

	dir := t.TempDir()
	pa := filepath.Join(dir, "a.txt")
	pb := filepath.Join(dir, "b.txt")
	pbig := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(pa, smallA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pb, smallB, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pbig, big, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sender.SendFiles(TransferRequest{
		DeviceAddr: server.Listener.Addr().String(),
		FilePaths:  []string{pa, pb, pbig},
	}); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}

	check := func(name string, want []byte) {
		got, err := os.ReadFile(filepath.Join(downloadDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s mismatch (%d vs %d bytes)", name, len(got), len(want))
		}
	}
	check("a.txt", smallA)
	check("b.txt", smallB)
	check("big.bin", big)
}
