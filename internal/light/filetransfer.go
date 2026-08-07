package light

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

const chunkSize = 1 << 20 // 1MB

type acceptState struct {
	status string // pending | accepted | rejected | cancelled
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
	app      *application.App
	manager  *TransferManager
	settings *SettingsService
	discovery *DiscoveryService

	mu      sync.Mutex
	server  *http.Server
	ln      net.Listener
	mux     *http.ServeMux
	accepts map[string]*acceptState
	controls map[string]*sendControl
}

func NewFileTransferService(app *application.App, manager *TransferManager, settings *SettingsService, discovery *DiscoveryService) *FileTransferService {
	return &FileTransferService{
		app:      app,
		manager:  manager,
		settings: settings,
		discovery: discovery,
		accepts:  make(map[string]*acceptState),
		controls: make(map[string]*sendControl),
	}
}

func (s *FileTransferService) SetApp(app *application.App) { s.app = app }

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
	s.mux = mux
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.ln = ln
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	s.mu.Unlock()
	go s.server.Serve(ln)
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
	s.ln = nil
	s.server = nil
	s.mu.Unlock()
}

func (s *FileTransferService) RestartServer() {
	s.StopServer()
	_ = s.StartServer()
	s.emitSettings()
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
	s.mu.Unlock()
	if !accepted {
		http.Error(w, "not accepted", 409)
		return
	}

	dl := s.receiveDir()
	if mkErr := os.MkdirAll(dl, 0o755); mkErr != nil {
		http.Error(w, "cannot create destination dir: "+mkErr.Error(), 500)
		return
	}
	path := uniquePath(filepath.Join(dl, sanitize(fname)))
	// Record the real destination the user chose. On mobile the file is first
	// written to an app-internal staging dir (SAF folders aren't raw-writable
	// under scoped storage); the frontend then bridges the finished file into
	// the chosen folder via the SAF tree URI.
	dest := s.settings.GetSettings().DownloadDir
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "cannot open destination ("+dl+"): "+err.Error(), 500)
		return
	}
	defer f.Close()
	if offset > 0 {
		_, _ = f.Seek(offset, 0)
	}

	subID := tid + ":" + fname
	s.manager.RecordTransfer(&Transfer{ID: subID, Filename: fname, Size: size, Status: StatusActive})

	h := sha256.New()
	var written int64
	start := time.Now()
	lastEmit := start
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
			h.Write(buf[:n])
			written += int64(n)
			offset += int64(n)
			if time.Since(lastEmit) > 200*time.Millisecond {
				elapsed := time.Since(start).Seconds()
				speed := int64(0)
				if elapsed > 0 {
					speed = int64(float64(written) / elapsed)
				}
				s.manager.UpdateProgress(subID, offset, speed)
				if s.app != nil {
					s.app.Event.Emit("transfer-progress", map[string]any{
						"id": subID, "filename": fname, "transferred": offset, "size": size, "speed": speed,
					})
				}
				lastEmit = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			s.failTransfer(subID, fname, err.Error())
			http.Error(w, "read error", 400)
			return
		}
	}

	_ = f.Sync()
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != checksum {
		s.failTransfer(subID, fname, "checksum mismatch")
		http.Error(w, "checksum mismatch", 400)
		return
	}
	s.manager.Complete(subID, path, sum)
	if s.app != nil {
		s.app.Event.Emit("transfer-complete", map[string]any{
			"id": subID, "filename": fname, "filePath": path, "checksum": sum,
			"destination": dest, "destinationUri": s.settings.GetSettings().DownloadDirUri,
		})
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// receiveDir returns the directory the receiver writes incoming files to. On
// mobile the chosen SAF folder is not raw-filesystem-writable under scoped
// storage, so we stage into an app-internal dir; desktop honours DownloadDir.
func (s *FileTransferService) receiveDir() string {
	if PlatformDeviceType() == DeviceTypeMobile {
		dir := filepath.Join(configDir(), "downloads")
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			// Fallback: try a temp dir — at least the transfer won't 500.
			dir = filepath.Join(os.TempDir(), "light-downloads")
			_ = os.MkdirAll(dir, 0o755)
		}
		return dir
	}
	return s.settings.GetSettings().DownloadDir
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
		sum, err := sha256File(p)
		if err != nil {
			continue
		}
		entries = append(entries, FileManifestEntry{Name: filepath.Base(p), Size: info.Size(), Checksum: sum})
		paths = append(paths, p)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no files to send")
	}

	tid := newID()
	p := PreparePayload{TransferID: tid, SenderName: s.settings.GetSettings().DeviceName, Files: entries}
	body, _ := json.Marshal(p)
	resp, err := http.Post("http://"+req.DeviceAddr+"/api/prepare", "application/json", bytes.NewReader(body))
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
		if !s.waitAccept(req.DeviceAddr, tid) {
			for _, e := range entries {
				s.failTransfer(tid+":"+e.Name, e.Name, "rejected or timed out")
			}
			return fmt.Errorf("transfer not accepted")
		}
	}

	for i, p2 := range paths {
		if err := s.upload(tid, req.DeviceAddr, p2, entries[i].Name, entries[i].Size, entries[i].Checksum); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileTransferService) waitAccept(peerAddr, tid string) bool {
	deadline := time.Now().Add(2 * time.Minute)
	url := "http://" + peerAddr + "/api/status/" + tid
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
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

func (s *FileTransferService) upload(tid, peerAddr, filePath, fname string, size int64, sum string) error {
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
		started: time.Now(), lastEmit: time.Now(), app: s.app, manager: s.manager,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://"+peerAddr+"/api/transfer", cr)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		return err
	}
	req.Header.Set("X-Transfer-Id", tid)
	req.Header.Set("X-Filename", fname)
	req.Header.Set("X-File-Size", strconv.FormatInt(size, 10))
	req.Header.Set("X-Offset", "0")
	req.Header.Set("X-Checksum-Sha256", sum)
	req.ContentLength = size

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
		} else {
			s.failTransfer(subID, fname, err.Error())
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		s.failTransfer(subID, fname, fmt.Sprintf("receiver error %d", resp.StatusCode))
		return fmt.Errorf("receiver error %d", resp.StatusCode)
	}

	s.manager.Complete(subID, "", sum)
	if s.app != nil {
		s.app.Event.Emit("transfer-complete", map[string]any{
			"id": subID, "filename": fname, "filePath": "", "checksum": sum,
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
	r        io.Reader
	ctrl     *sendControl
	subID    string
	fname    string
	size     int64
	started  time.Time
	lastEmit time.Time
	sent     int64
	app      *application.App
	manager  *TransferManager
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
		c.sent += int64(n)
		elapsed := time.Since(c.started).Seconds()
		if elapsed > 0 {
			speed := int64(float64(c.sent) / elapsed)
			c.manager.UpdateProgress(c.subID, c.sent, speed)
			now := time.Now()
			if now.Sub(c.lastEmit) > 200*time.Millisecond {
				c.lastEmit = now
				if c.app != nil {
					c.app.Event.Emit("transfer-progress", map[string]any{
						"id": c.subID, "filename": c.fname, "transferred": c.sent, "size": c.size, "speed": speed,
					})
				}
			}
		}
	}
	return n, err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
