package light

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	chunkSize             = 1 << 20 // 1 MiB receiver copy buffer
	partialFileMaxAge     = 24 * time.Hour
	maxParallelUploads    = 4
	mobileParallelUploads = 2
	progressInterval      = 200 * time.Millisecond
)

type acceptState struct {
	status     string // pending | accepted | rejected | cancelled
	senderID   string
	senderAddr string
	senderType DeviceType
}

// sendControl governs a single outgoing file upload (pause/resume/cancel).
type sendControl struct {
	mu       sync.Mutex
	status   TransferStatus
	cancel   context.CancelFunc
	resumeCh chan struct{}
}

// FileTransferService runs a per-device HTTP server that receives files and
// drives outgoing uploads to peers.
type FileTransferService struct {
	app       *application.App
	manager   *TransferManager
	settings  *SettingsService
	discovery *DiscoveryService

	mu         sync.Mutex
	server     *http.Server
	ln         net.Listener
	mux        *http.ServeMux
	tcpClient  *http.Client
	quic       quicHTTP3Server
	quicConn   net.PacketConn
	quicClient *http.Client
	quicClose  func()
	accepts    map[string]*acceptState
	controls   map[string]*sendControl
}

func NewFileTransferService(app *application.App, manager *TransferManager, settings *SettingsService, discovery *DiscoveryService) *FileTransferService {
	return &FileTransferService{
		app:       app,
		manager:   manager,
		settings:  settings,
		discovery: discovery,
		tcpClient: newTCPClient(),
		accepts:   make(map[string]*acceptState),
		controls:  make(map[string]*sendControl),
	}
}

func (s *FileTransferService) SetApp(app *application.App) { s.app = app }

func newTCPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 8,
			MaxConnsPerHost:     8,
			IdleConnTimeout:     90 * time.Second,
			WriteBufferSize:     64 << 10,
			ReadBufferSize:      64 << 10,
			DisableCompression:  true,
		},
		Timeout: 0,
	}
}

// ---- HTTP server (receiver side) ----

func (s *FileTransferService) StartServer() error {
	s.mu.Lock()
	if s.ln != nil {
		s.mu.Unlock()
		return nil
	}
	port := s.settings.GetSettings().Port
	mux := http.NewServeMux()
	mux.HandleFunc("/api/prepare", s.handlePrepare)
	mux.HandleFunc("/api/transfer", s.handleTransfer)
	mux.HandleFunc("/api/status/", s.handleStatus)
	mux.HandleFunc("/api/capabilities", s.handleCapabilities)
	s.mux = mux
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		s.mu.Unlock()
		return err
	}

	var quicServer quicHTTP3Server
	var quicConn net.PacketConn
	if normalizeTransportMode(s.settings.GetSettings().TransportMode) == "quic" {
		quicServer, err = newQUICServer(fmt.Sprintf(":%d", port), mux)
		if err == nil {
			quicConn, err = net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		}
		if err != nil {
			// Keep the stable TCP listener alive when UDP/QUIC is unavailable.
			// Outgoing peers will fail the QUIC probe and use TCP instead.
			if quicConn != nil {
				_ = quicConn.Close()
			}
			quicServer = nil
			quicConn = nil
			err = nil
		}
	}
	s.ln = ln
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	s.quic = quicServer
	s.quicConn = quicConn
	s.mu.Unlock()
	go s.server.Serve(ln)
	if quicServer != nil {
		go func() {
			_ = quicServer.Serve(quicConn)
		}()
	}
	return nil
}

func (s *FileTransferService) StopServer() {
	s.mu.Lock()
	if s.server != nil {
		_ = s.server.Close()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	if s.quic != nil {
		_ = s.quic.Close()
	}
	if s.quicConn != nil {
		_ = s.quicConn.Close()
	}
	if s.quicClose != nil {
		s.quicClose()
	}
	s.ln = nil
	s.server = nil
	s.quic = nil
	s.quicConn = nil
	s.quicClient = nil
	s.quicClose = nil
	s.mu.Unlock()
}

func (s *FileTransferService) RestartServer() error {
	s.StopServer()
	if err := s.StartServer(); err != nil {
		return err
	}
	s.emitSettings()
	return nil
}

// ---- Receiver-side handlers ----

func (s *FileTransferService) handlePrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var p PreparePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad payload", 400)
		return
	}
	if p.TransferID == "" {
		http.Error(w, "missing transferId", 400)
		return
	}

	s.mu.Lock()
	st, ok := s.accepts[p.TransferID]
	if !ok {
		st = &acceptState{status: "pending"}
		s.accepts[p.TransferID] = st
	}
	st.senderID = p.SenderID
	st.senderAddr = p.SenderAddr
	st.senderType = p.SenderType
	s.mu.Unlock()

	for _, f := range p.Files {
		s.manager.RecordTransfer(&Transfer{
			ID:       p.TransferID + ":" + f.Name,
			Filename: f.Name,
			Size:     f.Size,
			Status:   StatusPending,
		})
	}

	if s.settings.GetSettings().AutoAccept {
		s.mu.Lock()
		s.accepts[p.TransferID].status = "accepted"
		s.mu.Unlock()
		w.WriteHeader(200)
		w.Write([]byte("accepted"))
		return
	}

	if s.app != nil {
		s.app.Event.Emit("prepare-receive", p)
	}
	w.WriteHeader(202)
	w.Write([]byte("pending"))
}

func (s *FileTransferService) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.URL.Path)
	s.mu.Lock()
	st := s.accepts[id]
	s.mu.Unlock()
	if st == nil {
		w.WriteHeader(404)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte(st.status))
}

func (s *FileTransferService) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"transport":   "quic",
		"tcpFallback": true,
	})
}

func (s *FileTransferService) handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	tid := r.Header.Get("X-Transfer-Id")
	fname := r.Header.Get("X-Filename")
	size := atoi64(r.Header.Get("X-File-Size"))
	offset := atoi64(r.Header.Get("X-Offset"))
	checksum := r.Header.Get("X-Checksum-Sha256")

	s.mu.Lock()
	st := s.accepts[tid]
	accepted := st != nil && st.status == "accepted"
	senderID := ""
	senderAddr := ""
	senderType := DeviceType("")
	if st != nil {
		senderID = st.senderID
		senderAddr = st.senderAddr
		senderType = st.senderType
	}
	s.mu.Unlock()
	if !accepted {
		http.Error(w, "not accepted", 409)
		return
	}

	dl, err := s.receiveDir()
	if err != nil {
		http.Error(w, "cannot create destination dir: "+err.Error(), 500)
		return
	}
	path := uniquePath(filepath.Join(dl, sanitize(fname)))
	partialPath := path + ".light-partial-" + sanitize(tid)
	// Record the real destination the user chose. On mobile the file is first
	// written to an app-internal staging dir (SAF folders aren't raw-writable
	// under scoped storage); the frontend then bridges the finished file into
	// the chosen folder via the SAF tree URI.
	dest := s.settings.GetSettings().DownloadDir
	f, err := os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "cannot open destination ("+dl+"): "+err.Error(), 500)
		return
	}
	completed := false
	defer func() {
		_ = f.Close()
		if !completed {
			_ = os.Remove(partialPath)
		}
	}()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			s.failTransfer(tid+":"+fname, fname, err.Error())
			http.Error(w, "cannot seek destination: "+err.Error(), 500)
			return
		}
	}

	subID := tid + ":" + fname
	s.manager.RecordTransfer(&Transfer{ID: subID, Filename: fname, Size: size, Status: StatusActive})

	h := sha256.New()
	receiver := &receivingWriter{
		dst:          io.MultiWriter(f, h),
		subID:        subID,
		fname:        fname,
		size:         size,
		offset:       offset,
		started:      time.Now(),
		lastProgress: time.Now(),
		lastEmit:     time.Now(),
		app:          s.app,
		manager:      s.manager,
	}
	written, err := io.CopyBuffer(receiver, r.Body, make([]byte, chunkSize))
	receiver.reportProgress(true)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		http.Error(w, "read/write error", 400)
		return
	}

	if rawSize := strings.TrimSpace(r.Header.Get("X-File-Size")); rawSize != "" && written != size {
		errMsg := fmt.Sprintf("size mismatch: received %d bytes, expected %d", written, size)
		s.failTransfer(subID, fname, errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	if err := f.Sync(); err != nil {
		s.failTransfer(subID, fname, err.Error())
		http.Error(w, "sync error", 500)
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if checksum != "" && !strings.EqualFold(sum, checksum) {
		s.failTransfer(subID, fname, "checksum mismatch")
		http.Error(w, "checksum mismatch", 400)
		return
	}
	if err := f.Close(); err != nil {
		s.failTransfer(subID, fname, err.Error())
		http.Error(w, "close error", 500)
		return
	}
	if err := os.Rename(partialPath, path); err != nil {
		s.failTransfer(subID, fname, err.Error())
		http.Error(w, "finalize error", 500)
		return
	}
	completed = true
	s.manager.Complete(subID, path, sum)
	if s.app != nil {
		s.app.Event.Emit("transfer-complete", map[string]any{
			"id": subID, "filename": fname, "size": size, "filePath": path, "checksum": sum,
			"destination": dest, "destinationUri": s.settings.GetSettings().DownloadDirUri,
			"senderId": senderID, "senderAddr": senderAddr, "senderType": senderType,
		})
	}
	w.WriteHeader(200)
	_, _ = fmt.Fprintf(w, "ok %s", sum)
}

// receiveDir returns the directory the receiver writes incoming files to. On
// mobile the chosen SAF folder is not raw-filesystem-writable under scoped
// storage, so we stage into an app-internal dir; desktop honours DownloadDir.
func (s *FileTransferService) receiveDir() (string, error) {
	if PlatformDeviceType() == DeviceTypeMobile {
		var lastErr error
		for _, dir := range []string{
			filepath.Join(configDir(), "downloads"),
			filepath.Join(os.TempDir(), "light-downloads"),
		} {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr == nil {
				cleanupPartialFiles(dir)
				return dir, nil
			} else {
				lastErr = mkErr
			}
		}
		return "", fmt.Errorf("no writable staging directory: %w", lastErr)
	}
	dir := s.settings.GetSettings().DownloadDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	cleanupPartialFiles(dir)
	return dir, nil
}

func cleanupPartialFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-partialFileMaxAge)
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), ".light-partial-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// ---- Sender side ----

func (s *FileTransferService) SendFiles(req TransferRequest) error {
	if req.DeviceAddr == "" {
		return fmt.Errorf("no device address")
	}
	var entries []FileManifestEntry
	var paths []string
	for _, p := range req.FilePaths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		entries = append(entries, FileManifestEntry{Name: filepath.Base(p), Size: info.Size()})
		paths = append(paths, p)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no files to send")
	}

	tid := newID()
	p := PreparePayload{
		TransferID: tid,
		SenderID:   s.settings.DeviceID(),
		SenderName: s.settings.GetSettings().DeviceName,
		SenderAddr: s.localEndpoint(),
		SenderType: PlatformDeviceType(),
		Files:      entries,
	}
	client, scheme, closeClient, err := s.clientForPeer(req.DeviceAddr)
	if err != nil {
		return err
	}
	defer closeClient()
	body, _ := json.Marshal(p)
	resp, err := client.Post(scheme+"://"+req.DeviceAddr+"/api/prepare", "application/json", bytes.NewReader(body))
	if err != nil {
		for _, e := range entries {
			s.failTransfer(tid+":"+e.Name, e.Name, err.Error())
		}
		return err
	}
	statusBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	status := strings.TrimSpace(string(statusBytes))

	if status == "rejected" || status == "cancelled" {
		for _, e := range entries {
			s.failTransfer(tid+":"+e.Name, e.Name, "rejected by receiver")
		}
		return fmt.Errorf("rejected by receiver")
	}
	if status == "pending" {
		if !s.waitAccept(req.DeviceAddr, tid, client, scheme) {
			for _, e := range entries {
				s.failTransfer(tid+":"+e.Name, e.Name, "rejected or timed out")
			}
			return fmt.Errorf("transfer not accepted")
		}
	}

	semaphore := make(chan struct{}, s.parallelUploadLimit())
	var wg sync.WaitGroup
	var failuresMu sync.Mutex
	var failures []string

	for i, p2 := range paths {
		entry := entries[i]
		wg.Add(1)
		go func(path string, file FileManifestEntry) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			if err := s.uploadWithClient(tid, req.DeviceAddr, path, file.Name, file.Size, file.Checksum, client, scheme); err != nil {
				failuresMu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", file.Name, err))
				failuresMu.Unlock()
			}
		}(p2, entry)
	}
	wg.Wait()
	if len(failures) > 0 {
		return fmt.Errorf("%d file(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func (s *FileTransferService) parallelUploadLimit() int {
	if PlatformDeviceType() == DeviceTypeMobile {
		return mobileParallelUploads
	}
	return maxParallelUploads
}

func (s *FileTransferService) localEndpoint() string {
	if s.discovery != nil {
		return s.discovery.LocalEndpoint()
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(s.settings.GetSettings().Port))
}

func (s *FileTransferService) waitAccept(peerAddr, tid string, client *http.Client, scheme string) bool {
	deadline := time.Now().Add(2 * time.Minute)
	url := scheme + "://" + peerAddr + "/api/status/" + tid
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch strings.TrimSpace(string(b)) {
			case "accepted":
				return true
			case "rejected", "cancelled":
				return false
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func (s *FileTransferService) uploadWithClient(tid, peerAddr, filePath, fname string, size int64, _ string, client *http.Client, scheme string) error {
	subID := tid + ":" + fname
	ctx, cancel := context.WithCancel(context.Background())
	ctrl := &sendControl{status: StatusActive, resumeCh: make(chan struct{})}
	// Set ctrl.cancel under the lock before registering so a concurrent
	// CancelTransfer can never observe a zero cancel (or race on the field).
	ctrl.mu.Lock()
	ctrl.cancel = cancel
	ctrl.mu.Unlock()
	s.mu.Lock()
	s.controls[subID] = ctrl
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.controls, subID)
		s.mu.Unlock()
	}()
	defer cancel()

	s.manager.RecordTransfer(&Transfer{ID: subID, Filename: fname, Size: size, Status: StatusActive})

	f, err := os.Open(filePath)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		return err
	}
	defer f.Close()

	cr := &countingReader{
		r: f, ctrl: ctrl, subID: subID, fname: fname, size: size,
		started: time.Now(), lastProgress: time.Now(), lastEmit: time.Now(),
		app: s.app, manager: s.manager, hash: sha256.New(),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, scheme+"://"+peerAddr+"/api/transfer", cr)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		return err
	}
	req.Header.Set("X-Transfer-Id", tid)
	req.Header.Set("X-Filename", fname)
	req.Header.Set("X-File-Size", strconv.FormatInt(size, 10))
	req.Header.Set("X-Offset", "0")
	req.ContentLength = size

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
		} else {
			s.failTransfer(subID, fname, err.Error())
		}
		return err
	}
	cr.reportProgress(true)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		errMsg := fmt.Sprintf("receiver error %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		s.failTransfer(subID, fname, errMsg)
		return errors.New(errMsg)
	}
	receiverChecksum, err := parseTransferReceipt(strings.TrimSpace(string(respBody)))
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		return err
	}
	senderChecksum := cr.checksum()
	if !strings.EqualFold(receiverChecksum, senderChecksum) {
		errMsg := fmt.Sprintf("checksum mismatch: sender %s, receiver %s", senderChecksum, receiverChecksum)
		s.failTransfer(subID, fname, errMsg)
		return errors.New(errMsg)
	}

	s.manager.Complete(subID, "", senderChecksum)
	if s.app != nil {
		s.app.Event.Emit("transfer-complete", map[string]any{
			"id": subID, "filename": fname, "size": size, "filePath": "", "checksum": senderChecksum,
		})
	}
	return nil
}

// ---- Pause / resume / cancel ----

func (s *FileTransferService) PauseTransfer(id string) {
	s.mu.Lock()
	ctrl := s.controls[id]
	s.mu.Unlock()
	if ctrl != nil {
		ctrl.mu.Lock()
		if ctrl.status == StatusActive {
			ctrl.status = StatusPaused
		}
		ctrl.mu.Unlock()
	}
	s.manager.Pause(id)
	s.emit(map[string]string{"id": id}, "transfer-paused")
}

func (s *FileTransferService) ResumeTransfer(id string) {
	s.mu.Lock()
	ctrl := s.controls[id]
	s.mu.Unlock()
	if ctrl != nil {
		ctrl.mu.Lock()
		if ctrl.status == StatusPaused {
			ctrl.status = StatusActive
			old := ctrl.resumeCh
			ctrl.resumeCh = make(chan struct{})
			ctrl.mu.Unlock()
			close(old)
		} else {
			ctrl.mu.Unlock()
		}
	}
	s.manager.Resume(id)
	s.emit(map[string]string{"id": id}, "transfer-resume")
}

func (s *FileTransferService) CancelTransfer(id string) {
	s.mu.Lock()
	ctrl := s.controls[id]
	s.mu.Unlock()
	if ctrl != nil {
		var release chan struct{}
		ctrl.mu.Lock()
		wasPaused := ctrl.status == StatusPaused
		ctrl.status = StatusCancelled
		if ctrl.cancel != nil {
			ctrl.cancel()
		}
		if wasPaused {
			// Wake a reader blocked on pause so it observes StatusCancelled;
			// otherwise the request goroutine leaks forever. Safe: we changed
			// the status before unlocking, so ResumeTransfer can no longer
			// replace (and thus re-close) this channel.
			release = ctrl.resumeCh
		}
		ctrl.mu.Unlock()
		if release != nil {
			close(release)
		}
	}
	s.manager.Cancel(id)
	s.emit(map[string]string{"id": id}, "transfer-cancelled")
}

// ---- Accept / reject (receiver side) ----

func (s *FileTransferService) AcceptReceive(transferID string, _ []string) {
	s.mu.Lock()
	st, ok := s.accepts[transferID]
	if !ok {
		st = &acceptState{}
		s.accepts[transferID] = st
	}
	st.status = "accepted"
	s.mu.Unlock()
}

func (s *FileTransferService) RejectReceive(transferID string) {
	s.mu.Lock()
	st, ok := s.accepts[transferID]
	if !ok {
		st = &acceptState{}
		s.accepts[transferID] = st
	}
	st.status = "rejected"
	s.mu.Unlock()
}

// ---- helpers ----

func (s *FileTransferService) emit(payload any, name string) {
	if s.app != nil {
		s.app.Event.Emit(name, payload)
	}
}

func (s *FileTransferService) emitSettings() {
	if s.app != nil {
		s.app.Event.Emit("settings-changed", s.settings.GetSettings())
	}
}

func (s *FileTransferService) failTransfer(subID, fname, msg string) {
	s.manager.Fail(subID, msg)
	if s.app != nil {
		s.app.Event.Emit("transfer-error", map[string]any{"id": subID, "filename": fname, "error": msg})
	}
}

// countingReader reports upload progress and honours pause/cancel on the sender.
type countingReader struct {
	r            io.Reader
	hash         hash.Hash
	ctrl         *sendControl
	subID        string
	fname        string
	size         int64
	started      time.Time
	lastProgress time.Time
	lastEmit     time.Time
	sent         int64
	app          *application.App
	manager      *TransferManager
}

func (c *countingReader) Read(p []byte) (int, error) {
	for {
		c.ctrl.mu.Lock()
		st := c.ctrl.status
		ch := c.ctrl.resumeCh
		c.ctrl.mu.Unlock()
		if st == StatusCancelled {
			return 0, context.Canceled
		}
		if st == StatusPaused {
			<-ch
			continue
		}
		break
	}
	n, err := c.r.Read(p)
	if n > 0 {
		_, _ = c.hash.Write(p[:n])
		c.sent += int64(n)
		c.reportProgress(false)
	}
	return n, err
}

func (c *countingReader) reportProgress(force bool) {
	now := time.Now()
	if !force && now.Sub(c.lastProgress) < progressInterval {
		return
	}
	c.lastProgress = now
	elapsed := now.Sub(c.started).Seconds()
	speed := int64(0)
	if elapsed > 0 {
		speed = int64(float64(c.sent) / elapsed)
	}
	c.manager.UpdateProgress(c.subID, c.sent, speed)
	if c.app != nil && (force || now.Sub(c.lastEmit) >= progressInterval) {
		c.lastEmit = now
		c.app.Event.Emit("transfer-progress", map[string]any{
			"id": c.subID, "filename": c.fname, "transferred": c.sent, "size": c.size, "speed": speed,
		})
	}
}

func (c *countingReader) checksum() string {
	return hex.EncodeToString(c.hash.Sum(nil))
}

type receivingWriter struct {
	dst          io.Writer
	subID        string
	fname        string
	size         int64
	offset       int64
	written      int64
	started      time.Time
	lastProgress time.Time
	lastEmit     time.Time
	app          *application.App
	manager      *TransferManager
}

func (w *receivingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.written += int64(n)
		w.reportProgress(false)
	}
	return n, err
}

func (w *receivingWriter) reportProgress(force bool) {
	now := time.Now()
	if !force && now.Sub(w.lastProgress) < progressInterval {
		return
	}
	w.lastProgress = now
	elapsed := now.Sub(w.started).Seconds()
	speed := int64(0)
	if elapsed > 0 {
		speed = int64(float64(w.written) / elapsed)
	}
	transferred := w.offset + w.written
	w.manager.UpdateProgress(w.subID, transferred, speed)
	if w.app != nil && (force || now.Sub(w.lastEmit) >= progressInterval) {
		w.lastEmit = now
		w.app.Event.Emit("transfer-progress", map[string]any{
			"id": w.subID, "filename": w.fname, "transferred": transferred, "size": w.size, "speed": speed,
		})
	}
}

func parseTransferReceipt(body string) (string, error) {
	fields := strings.Fields(body)
	if len(fields) != 2 || fields[0] != "ok" {
		return "", fmt.Errorf("invalid receiver response: %q", body)
	}
	if len(fields[1]) != sha256.Size*2 {
		return "", fmt.Errorf("invalid receiver checksum")
	}
	if _, err := hex.DecodeString(fields[1]); err != nil {
		return "", fmt.Errorf("invalid receiver checksum: %w", err)
	}
	return strings.ToLower(fields[1]), nil
}

func atoi64(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func sanitize(name string) string {
	name = filepath.Base(name)
	return strings.ReplaceAll(name, "/", "_")
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}
