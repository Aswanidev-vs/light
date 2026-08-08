package light

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestQUICHTTP3Integration(t *testing.T) {
	downloadDir := t.TempDir()
	receiverManager := &TransferManager{active: make(map[string]*Transfer)}
	receiverSettings := &SettingsService{cfg: Settings{DownloadDir: downloadDir, AutoAccept: true}}
	receiver := NewFileTransferService(nil, receiverManager, receiverSettings, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", receiver.handlePrepare)
	mux.HandleFunc("/api/transfer", receiver.handleTransfer)
	mux.HandleFunc("/api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server, err := newQUICServer("127.0.0.1:0", mux)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = packetConn.Close()
	})
	go func() { _ = server.Serve(packetConn) }()

	client, closeClient, err := newQUICClient()
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	response, err := client.Get("https://" + packetConn.LocalAddr().String() + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("QUIC status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	payload := []byte("QUIC integration transfer")
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])
	transferID := "quic-integration"
	prepareBody, err := json.Marshal(PreparePayload{
		TransferID: transferID,
		SenderName: "quic sender",
		Files:      []FileManifestEntry{{Name: "quic.txt", Size: int64(len(payload)), Checksum: checksum}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse, err := client.Post("https://"+packetConn.LocalAddr().String()+"/api/prepare", "application/json", bytes.NewReader(prepareBody))
	if err != nil {
		t.Fatal(err)
	}
	prepareResponse.Body.Close()
	if prepareResponse.StatusCode != http.StatusOK {
		t.Fatalf("QUIC prepare status = %d, want %d", prepareResponse.StatusCode, http.StatusOK)
	}

	sourcePath := filepath.Join(t.TempDir(), "quic.txt")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	senderManager := &TransferManager{active: make(map[string]*Transfer)}
	sender := NewFileTransferService(nil, senderManager, &SettingsService{}, nil)
	if err := sender.uploadWithClient(transferID, packetConn.LocalAddr().String(), sourcePath, "quic.txt", int64(len(payload)), checksum, client, "https"); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(downloadDir, "quic.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payload) {
		t.Fatalf("stored QUIC payload = %q, want %q", stored, payload)
	}
}

func BenchmarkTransferTransport(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("/bench", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	tcpServer := httptest.NewServer(mux)
	b.Cleanup(tcpServer.Close)

	quicServer, err := newQUICServer("127.0.0.1:0", mux)
	if err != nil {
		b.Fatal(err)
	}
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = quicServer.Close()
		_ = packetConn.Close()
	})
	go func() { _ = quicServer.Serve(packetConn) }()

	quicClient, closeQUIC, err := newQUICClient()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(closeQUIC)
	payload := bytes.Repeat([]byte("light-transfer-benchmark-"), 1<<16)

	benchmark := func(b *testing.B, client *http.Client, endpoint string) {
		b.Helper()
		b.SetBytes(int64(len(payload)))
		for i := 0; i < b.N; i++ {
			request, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(payload))
			if err != nil {
				b.Fatal(err)
			}
			request.ContentLength = int64(len(payload))
			response, err := client.Do(request)
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}

	b.Run("tcp", func(b *testing.B) {
		benchmark(b, http.DefaultClient, tcpServer.URL+"/bench")
	})
	b.Run("quic", func(b *testing.B) {
		benchmark(b, quicClient, "https://"+packetConn.LocalAddr().String()+"/bench")
	})
}
