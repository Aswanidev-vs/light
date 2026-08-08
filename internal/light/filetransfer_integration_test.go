package light

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		SenderName: "integration sender",
		Files:      []FileManifestEntry{{Name: filename, Size: int64(len(payload)), Checksum: checksum}},
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

	transferRequest, err := http.NewRequest(http.MethodPut, server.URL+"/api/transfer", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	transferRequest.Header.Set("X-Transfer-Id", transferID)
	transferRequest.Header.Set("X-Filename", filename)
	transferRequest.Header.Set("X-File-Size", strconv.FormatInt(int64(len(payload)), 10))
	transferRequest.Header.Set("X-Checksum-Sha256", checksum)
	transferResponse, err := http.DefaultClient.Do(transferRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer transferResponse.Body.Close()
	if transferResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(transferResponse.Body)
		t.Fatalf("transfer status = %d, want %d: %s", transferResponse.StatusCode, http.StatusOK, strings.TrimSpace(string(body)))
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
