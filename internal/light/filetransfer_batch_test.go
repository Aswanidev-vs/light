package light

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newBatchPair starts a receiver writing into downloadDir and returns it with a
// sender pointed at it. autoAccept mirrors the AutoAccept setting, which lets a
// batch proceed without the accept prompt.
func newBatchPair(t *testing.T, downloadDir string, autoAccept bool) (*FileTransferService, *httptest.Server, *FileTransferService) {
	t.Helper()
	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: autoAccept}}
	receiver := NewFileTransferService(nil, manager, settings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", receiver.handlePrepare)
	mux.HandleFunc("/api/status/", receiver.handleStatus)
	mux.HandleFunc("/api/transfer", receiver.handleTransfer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	sender := NewFileTransferService(nil, &TransferManager{active: make(map[string]*Transfer)}, &SettingsService{}, nil)
	return receiver, server, sender
}

func writeSources(t *testing.T, names ...string) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 0, len(names))
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("payload of "+n), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func serverAddr(server *httptest.Server) string {
	return strings.TrimPrefix(server.URL, "http://")
}

func bulkFolders(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "Bulk-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// assertNotWritten fails if name exists anywhere under dir, including inside a
// batch subfolder.
func assertNotWritten(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		t.Fatalf("%s should not have been written", name)
	}
	for _, b := range bulkFolders(t, dir) {
		if _, err := os.Stat(filepath.Join(dir, b, name)); err == nil {
			t.Fatalf("%s should not have been written into %s", name, b)
		}
	}
}

// A single-file transfer must keep landing flat, exactly as it did before batch
// folders existed.
func TestSingleFileBatchLandsFlat(t *testing.T) {
	downloadDir := t.TempDir()
	_, server, sender := newBatchPair(t, downloadDir, true)

	if err := sender.SendFiles(TransferRequest{
		DeviceAddr: serverAddr(server),
		FilePaths:  writeSources(t, "solo.txt"),
	}); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}

	if _, err := os.Stat(filepath.Join(downloadDir, "solo.txt")); err != nil {
		t.Fatalf("single file should land flat in the download dir: %v", err)
	}
	if folders := bulkFolders(t, downloadDir); len(folders) != 0 {
		t.Fatalf("single-file transfer created a batch folder %v", folders)
	}
}

// A multi-file batch must land together in one dated subfolder so a large
// transfer does not scatter across the download directory.
func TestMultiFileBatchLandsInDatedFolder(t *testing.T) {
	downloadDir := t.TempDir()
	_, server, sender := newBatchPair(t, downloadDir, true)

	names := []string{"one.txt", "two.txt", "three.txt"}
	if err := sender.SendFiles(TransferRequest{
		DeviceAddr: serverAddr(server),
		FilePaths:  writeSources(t, names...),
	}); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}

	folders := bulkFolders(t, downloadDir)
	if len(folders) != 1 {
		t.Fatalf("got %d batch folders, want exactly 1", len(folders))
	}
	if want := "Bulk-" + time.Now().Format("2006-01-02"); folders[0] != want {
		t.Fatalf("batch folder = %q, want %q", folders[0], want)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(downloadDir, folders[0], n)); err != nil {
			t.Fatalf("%s missing from batch folder: %v", n, err)
		}
	}
}

// TestPerFileDeselectSkipsOnlyUnticked covers the accept prompt's per-file
// consent: an unticked file must be skipped rather than reported as a failure,
// and the ticked ones must still arrive.
func TestPerFileDeselectSkipsOnlyUnticked(t *testing.T) {
	downloadDir := t.TempDir()
	receiver, server, sender := newBatchPair(t, downloadDir, false)

	const tid = "deselect-batch"
	entries := []FileManifestEntry{
		{Name: "keep.txt", Size: int64(len("payload of keep.txt"))},
		{Name: "drop.txt", Size: int64(len("payload of drop.txt"))},
	}
	// Register the batch, then narrow consent to keep.txt only, exactly as
	// AcceptReceive does from the accept prompt's checkboxes.
	receiver.mu.Lock()
	receiver.accepts[tid] = &acceptState{
		status: "pending",
		files:  []string{"keep.txt", "drop.txt"},
	}
	receiver.mu.Unlock()
	receiver.AcceptReceive(tid, []string{"keep.txt"})

	paths := writeSources(t, "keep.txt", "drop.txt")
	client := newTCPClient()
	for i, p := range paths {
		src, err := openSendSource(p)
		if err != nil {
			t.Fatal(err)
		}
		err = sender.uploadWithClient(tid, serverAddr(server), src, entries[i], client, "http")
		src.Close()
		if err != nil {
			// An unticked file must not surface as an error: SendFiles reports
			// skipped files separately from failed ones.
			t.Fatalf("%s should have been skipped, not failed: %v", entries[i].Name, err)
		}
	}

	// The batch has two files, so the ticked one lands in the batch folder.
	got := readReceived(t, downloadDir, "keep.txt")
	if !bytes.Equal(got, []byte("payload of keep.txt")) {
		t.Fatalf("ticked file content = %q", got)
	}
	assertNotWritten(t, downloadDir, "drop.txt")
}

// A batch accepted with a nil selection (what an older client sends) must admit
// every file, so the change stays backward compatible.
func TestAcceptNilSelectionAllowsWholeBatch(t *testing.T) {
	st := &acceptState{status: "accepted", files: []string{"a.txt", "b.txt"}}
	if !st.permits("a.txt") || !st.permits("b.txt") {
		t.Fatal("a nil selection should permit every file")
	}
	st.allowed = map[string]bool{"a.txt": true}
	if st.permits("b.txt") {
		t.Fatal("b.txt should not be permitted")
	}
	if !st.permits("a.txt") {
		t.Fatal("a.txt should still be permitted")
	}
}
