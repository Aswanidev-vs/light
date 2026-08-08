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
		SenderName: "integration sender",
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
	if strings.TrimSpace(string(responseBody)) != "ok "+checksum {
		t.Fatalf("transfer response = %q, want %q", strings.TrimSpace(string(responseBody)), "ok "+checksum)
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

func TestFileTransferHTTPIntegrationRejectsReceiverDigestMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{AutoAccept: true}}
	service := NewFileTransferService(nil, manager, settings, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok "+strings.Repeat("0", sha256.Size*2))
	}))
	t.Cleanup(server.Close)

	transferID := "checksum-mismatch"
	filename := "broken.bin"
	payload := []byte("payload")
	sourcePath := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	err := service.upload(transferID, server.Listener.Addr().String(), sourcePath, filename, int64(len(payload)))
	if err == nil || err.Error() != "checksum mismatch" {
		t.Fatalf("upload error = %v, want checksum mismatch", err)
	}
	history := manager.GetHistory(1)
	if len(history) != 1 || history[0].Status != StatusFailed || history[0].Error != "checksum mismatch" {
		t.Fatalf("history = %#v, want checksum failure", history)
	}
}

func TestFileTransferHTTPIntegrationLimitsParallelUploads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	manager := &TransferManager{active: make(map[string]*Transfer)}
	settings := &SettingsService{cfg: Settings{DeviceName: "integration sender"}}
	service := NewFileTransferService(nil, manager, settings, nil)
	var active atomic.Int32
	var maxActive atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "accepted")
	})
	mux.HandleFunc("/api/transfer", func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		defer active.Add(-1)
		data, _ := io.ReadAll(r.Body)
		time.Sleep(50 * time.Millisecond)
		sum := sha256.Sum256(data)
		if r.Header.Get("X-Filename") == "parallel-0.bin" {
			sum = sha256.Sum256([]byte("wrong digest"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok "+hex.EncodeToString(sum[:]))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	fileDir := t.TempDir()
	paths := make([]string, 0, maxConcurrentUploads+2)
	for i := 0; i < maxConcurrentUploads+2; i++ {
		path := filepath.Join(fileDir, "parallel-"+strconv.Itoa(i)+".bin")
		if err := os.WriteFile(path, []byte("parallel payload "+strconv.Itoa(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}

	err := service.SendFiles(TransferRequest{
		DeviceAddr: server.Listener.Addr().String(),
		FilePaths:  paths,
	})
	if err == nil {
		t.Fatal("SendFiles error = nil, want one per-file error")
	}
	if got := maxActive.Load(); got != maxConcurrentUploads {
		t.Fatalf("max concurrent uploads = %d, want %d", got, maxConcurrentUploads)
	}
	history := manager.GetHistory(len(paths))
	if len(history) != len(paths) {
		t.Fatalf("history entries = %d, want %d", len(history), len(paths))
	}
	var failed, completed int
	for _, transfer := range history {
		switch transfer.Status {
		case StatusFailed:
			failed++
		case StatusCompleted:
			completed++
		}
	}
	if failed != 1 || completed != len(paths)-1 {
		t.Fatalf("history statuses = %d failed, %d completed; want 1 failed and %d completed", failed, completed, len(paths)-1)
	}
}
