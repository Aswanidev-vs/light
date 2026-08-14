package light

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	chunkSize          = 1 << 20 // 1 MiB receiver copy buffer
	partialFileMaxAge  = 24 * time.Hour
	maxParallelUploads = 4
	// mobileParallelUploads is deliberately lower than maxParallelUploads to cut
	// CPU, memory, and battery pressure on phones, which also pay a
	// scoped-storage (SAF) staging cost on the receive side.
	mobileParallelUploads = 2
	progressInterval      = 200 * time.Millisecond
	// Files at least this large are split into parallel ranges so a single file
	// can saturate more than one stream/connection.
	segmentMinSize = 16 << 20 // 16 MiB
)

var transferBufferPool = sync.Pool{
	New: func() any { return make([]byte, chunkSize) },
}

type acceptState struct {
	status     string // pending | accepted | rejected | cancelled
	senderID   string
	senderAddr string
	senderType DeviceType
}

// ctrlStatus* mirror TransferStatus for lock-free reads on the upload hot path.
const (
	ctrlStatusActive int32 = iota
	ctrlStatusPaused
	ctrlStatusCancelled
)

// sendControl governs a single outgoing file upload (pause/resume/cancel).
type sendControl struct {
	mu           sync.Mutex
	status       TransferStatus
	statusAtomic int32 // atomic mirror of status; read without the mutex in Read
	cancel       context.CancelFunc
	resumeCh     chan struct{}
}

// assemblyState tracks the in-progress reassembly of a segmented (parallel-range)
// upload for one file on the receiver, keyed by transfer id + filename.
type assemblyState struct {
	mu           sync.Mutex
	totalSize    int64
	segmentCount int
	received     int
	partialPath  string
	finalPath    string
	checksum     string
	failed       bool
}

// FileTransferService runs a per-device HTTP server that receives files and
// drives outgoing uploads to peers.
type FileTransferService struct {
	app       *application.App
	manager   *TransferManager
	settings  *SettingsService
	discovery *DiscoveryService

	// deviceType overrides PlatformDeviceType() for tests; empty uses the
	// real platform type.
	deviceType  DeviceType
	mu          sync.Mutex
	server      *http.Server
	ln          net.Listener
	mux         *http.ServeMux
	tcpClient   *http.Client
	quic        quicHTTP3Server
	quicConn    net.PacketConn
	quicClient  *http.Client
	quicClose   func()
	quicProbes  map[string]quicProbeState
	accepts     map[string]*acceptState
	controls    map[string]*sendControl
	cleanedDirs sync.Map
	// assemblies tracks in-progress reassembly of segmented (parallel-range)
	// uploads on the receiver, keyed by transfer id + filename.
	assemblies sync.Map
}

func NewFileTransferService(app *application.App, manager *TransferManager, settings *SettingsService, discovery *DiscoveryService) *FileTransferService {
	return &FileTransferService{
		app:        app,
		manager:    manager,
		settings:   settings,
		discovery:  discovery,
		tcpClient:  newTCPClient(),
		accepts:    make(map[string]*acceptState),
		controls:   make(map[string]*sendControl),
		quicProbes: make(map[string]quicProbeState),
	}
}

func (s *FileTransferService) SetApp(app *application.App) { s.app = app }

func newTCPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				Control:   transferSocketControl,
			}).DialContext,
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 8,
			MaxConnsPerHost:     8,
			IdleConnTimeout:     90 * time.Second,
			WriteBufferSize:     1 << 20,
			ReadBufferSize:      1 << 20,
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
	lc := socketListenConfig()
	ln, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		s.mu.Unlock()
		return err
	}

	var quicServer quicHTTP3Server
	var quicConn net.PacketConn
	if normalizeTransportMode(s.settings.GetSettings().TransportMode) == "quic" {
		quicServer, err = newQUICServer(fmt.Sprintf(":%d", port), mux)
		if err == nil {
			quicConn, err = (&net.ListenConfig{Control: transferSocketControl}).ListenPacket(context.Background(), "udp", fmt.Sprintf(":%d", port))
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
	s.quicProbes = make(map[string]quicProbeState)
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
	// Use the address that actually reached this receiver for reverse sharing.
	// The sender's first local interface can be an unreachable APIPA adapter,
	// while the prepare request already proves which peer address is reachable.
	p.SenderAddr = observedPeerAddress(r, p.SenderAddr, s.settings.GetSettings().Port)

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

	// Segmented (parallel-range) uploads are reassembled separately so a single
	// large file can be written from multiple concurrent requests.
	segIndex := atoi64(r.Header.Get("X-Segment-Index"))
	segCount := atoi64(r.Header.Get("X-Segment-Count"))
	if segCount > 1 {
		s.handleSegmentedTransfer(w, r, tid, fname, size, offset, segIndex, segCount, checksum, senderID, senderAddr, senderType)
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
	// Single-stream transfers always start at offset 0 (the segmented path
	// handles ranged writes). Truncate any leftover partial so a retried request
	// can't leave stale trailing bytes behind.
	f, err := os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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

	subID := tid + ":" + fname
	s.manager.RecordTransfer(&Transfer{ID: subID, Filename: fname, Size: size, Status: StatusActive})

	h := sha256.New()
	receiver := &receivingWriter{
		dst:      io.MultiWriter(f, h),
		subID:    subID,
		fname:    fname,
		size:     size,
		offset:   offset,
		started:  time.Now(),
		lastEmit: time.Now(),
		app:      s.app,
		manager:  s.manager,
	}
	buffer := transferBufferPool.Get().([]byte)
	defer transferBufferPool.Put(buffer)
	written, err := io.CopyBuffer(receiver, r.Body, buffer)
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
				s.cleanupPartialFilesOnce(dir)
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
	s.cleanupPartialFilesOnce(dir)
	return dir, nil
}

func (s *FileTransferService) cleanupPartialFilesOnce(dir string) {
	if _, loaded := s.cleanedDirs.LoadOrStore(dir, struct{}{}); loaded {
		return
	}
	cleanupPartialFiles(dir)
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

// parallelUploadLimit returns the maximum number of files uploaded concurrently.
// Mobile devices use a lower limit than desktop to cut CPU, memory, and battery
// pressure; they also pay a scoped-storage (SAF) staging cost on the receive
// side. The result is clamped to at least one so a misconfigured limit can
// never deadlock the semaphore-based upload loop.
func (s *FileTransferService) parallelUploadLimit() int {
	dev := s.deviceType
	if dev == "" {
		dev = PlatformDeviceType()
	}
	limit := maxParallelUploads
	if dev == DeviceTypeMobile {
		limit = mobileParallelUploads
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

func (s *FileTransferService) localEndpoint() string {
	if s.discovery != nil {
		return s.discovery.LocalEndpoint()
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(s.settings.GetSettings().Port))
}

func observedPeerAddress(r *http.Request, advertised string, fallbackPort int) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || host == "" {
		return advertised
	}
	if ip := net.ParseIP(host); ip == nil {
		return advertised
	} else if v4 := ip.To4(); v4 != nil {
		host = v4.String()
	}

	port := strconv.Itoa(fallbackPort)
	if _, advertisedPort, err := net.SplitHostPort(strings.TrimSpace(advertised)); err == nil && advertisedPort != "" {
		port = advertisedPort
	}
	return net.JoinHostPort(host, port)
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

	// Hash the whole file in the background from a second handle so the
	// SHA-256 cost doesn't sit on the network copy hot path.
	hashFile, herr := os.Open(filePath)
	var senderChecksum string
	var hashErr error
	hashDone := make(chan struct{})
	if herr == nil {
		go func() {
			defer close(hashDone)
			defer hashFile.Close()
			h := sha256.New()
			if _, e := io.Copy(h, hashFile); e != nil {
				hashErr = e
				return
			}
			senderChecksum = hex.EncodeToString(h.Sum(nil))
		}()
	} else {
		close(hashDone)
	}

	// Split large files into parallel ranges so a single file can saturate more
	// than one stream/connection. Small files stay single-stream.
	segCount := 1
	if size >= segmentMinSize {
		segCount = s.parallelUploadLimit()
	}
	fileStart := time.Now()

	if segCount <= 1 {
		rc, err := s.uploadRange(ctx, client, scheme, peerAddr, tid, fname, size, 0, size, filePath, subID, ctrl, 0, 1, fileStart, nil, "")
		if err != nil {
			return err
		}
		return s.verifyAndComplete(subID, fname, size, rc, senderChecksum, hashDone, &hashErr)
	}

	var totalSent int64
	segSize := size / int64(segCount)
	var wg sync.WaitGroup
	var failuresMu sync.Mutex
	var failures []string
	var checksumMu sync.Mutex
	var receiverChecksum string
	for i := 0; i < segCount; i++ {
		start := int64(i) * segSize
		end := start + segSize
		if i == segCount-1 {
			end = size
		}
		wg.Add(1)
		go func(i int, start, end int64) {
			defer wg.Done()
			rc, err := s.uploadRange(ctx, client, scheme, peerAddr, tid, fname, size, start, end-start, filePath, subID, ctrl, i, segCount, fileStart, &totalSent, senderChecksum)
			if err != nil {
				failuresMu.Lock()
				failures = append(failures, fmt.Sprintf("segment %d: %v", i, err))
				failuresMu.Unlock()
				return
			}
			if rc != "" {
				checksumMu.Lock()
				receiverChecksum = rc
				checksumMu.Unlock()
			}
		}(i, start, end)
	}
	wg.Wait()
	if len(failures) > 0 {
		s.failTransfer(subID, fname, strings.Join(failures, "; "))
		return fmt.Errorf("%d segment(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return s.verifyAndComplete(subID, fname, size, receiverChecksum, senderChecksum, hashDone, &hashErr)
}

// uploadRange streams one [offset, offset+length) slice of the file as its own
// HTTP request. For segmented transfers it carries X-Segment-* headers so the
// receiver can reassemble; the final/whole-file response carries the receiver's
// SHA-256, which the caller verifies against the sender's background hash.
func (s *FileTransferService) uploadRange(ctx context.Context, client *http.Client, scheme, peerAddr, tid, fname string, size, offset, length int64, filePath, subID string, ctrl *sendControl, segIndex, segCount int, started time.Time, totalSent *int64, expected string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		return "", err
	}
	defer f.Close()

	section := io.NewSectionReader(f, offset, length)
	cr := &countingReader{
		r:        section,
		ctrl:     ctrl,
		subID:    subID,
		fname:    fname,
		size:     size,
		started:  started,
		lastEmit: time.Now(),
		app:      s.app,
		manager:  s.manager,
		total:    totalSent,
	}
	defer cr.reportProgress(true)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, scheme+"://"+peerAddr+"/api/transfer", cr)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		return "", err
	}
	req.Header.Set("X-Transfer-Id", tid)
	req.Header.Set("X-Filename", fname)
	req.Header.Set("X-File-Size", strconv.FormatInt(size, 10))
	req.Header.Set("X-Offset", strconv.FormatInt(offset, 10))
	req.ContentLength = length
	if segCount > 1 {
		req.Header.Set("X-Segment-Index", strconv.FormatInt(int64(segIndex), 10))
		req.Header.Set("X-Segment-Count", strconv.FormatInt(int64(segCount), 10))
		req.Header.Set("X-Checksum-Sha256", expected)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
		} else {
			s.failTransfer(subID, fname, err.Error())
		}
		return "", err
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		errMsg := fmt.Sprintf("receiver error %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		s.failTransfer(subID, fname, errMsg)
		return "", errors.New(errMsg)
	}
	fields := strings.Fields(strings.TrimSpace(string(respBody)))
	if len(fields) == 0 || fields[0] != "ok" {
		errMsg := fmt.Sprintf("invalid receiver response: %q", strings.TrimSpace(string(respBody)))
		s.failTransfer(subID, fname, errMsg)
		return "", errors.New(errMsg)
	}
	if len(fields) >= 2 {
		return strings.ToLower(fields[1]), nil
	}
	return "", nil
}

// verifyAndComplete waits for the background hash, then checks the receiver's
// returned checksum against the sender's and marks the transfer complete.
func (s *FileTransferService) verifyAndComplete(subID, fname string, size int64, receiverChecksum, senderChecksum string, hashDone chan struct{}, hashErr *error) error {
	<-hashDone
	if *hashErr != nil {
		s.failTransfer(subID, fname, (*hashErr).Error())
		return *hashErr
	}
	if receiverChecksum == "" {
		s.failTransfer(subID, fname, "missing receiver checksum")
		return errors.New("missing receiver checksum")
	}
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

// handleSegmentedTransfer reassembles a file sent as parallel ranges. Each range
// arrives as its own /api/transfer request carrying X-Segment-* headers; all
// ranges for a (transfer id, filename) write into one partial file at their
// given offset, and the last range to land triggers the final hash verify,
// rename, and completion event.
func (s *FileTransferService) handleSegmentedTransfer(w http.ResponseWriter, r *http.Request, tid, fname string, size, offset, segIndex, segCount int64, checksum, senderID, senderAddr string, senderType DeviceType) {
	dl, err := s.receiveDir()
	if err != nil {
		http.Error(w, "cannot create destination dir: "+err.Error(), 500)
		return
	}
	key := tid + "\x00" + fname
	val, _ := s.assemblies.LoadOrStore(key, &assemblyState{
		totalSize:    size,
		segmentCount: int(segCount),
		finalPath:    uniquePath(filepath.Join(dl, sanitize(fname))),
	})
	as := val.(*assemblyState)
	as.mu.Lock()
	if as.partialPath == "" {
		as.partialPath = as.finalPath + ".light-partial-" + sanitize(tid)
		as.checksum = checksum
	}
	partialPath := as.partialPath
	expected := as.checksum
	as.mu.Unlock()

	subID := tid + ":" + fname
	s.manager.RecordTransfer(&Transfer{ID: subID, Filename: fname, Size: size, Status: StatusActive})

	f, err := os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.failTransfer(subID, fname, err.Error())
		http.Error(w, "cannot open destination: "+err.Error(), 500)
		return
	}
	if _, err := f.Seek(offset, 0); err != nil {
		_ = f.Close()
		s.failSegment(as, key, partialPath, subID, fname, err.Error())
		http.Error(w, "cannot seek destination: "+err.Error(), 500)
		return
	}
	buffer := transferBufferPool.Get().([]byte)
	defer transferBufferPool.Put(buffer)
	if _, err := io.CopyBuffer(f, r.Body, buffer); err != nil {
		_ = f.Close()
		s.failSegment(as, key, partialPath, subID, fname, err.Error())
		http.Error(w, "read/write error", 400)
		return
	}
	// Flush this segment's bytes before the final reassembly hash/rename; the
	// single-stream path does the same (f.Sync before rename). Syncing any one
	// handle flushes the whole file, so the completed rename is durable.
	_ = f.Sync()
	_ = f.Close()

	as.mu.Lock()
	as.received++
	last := as.received == as.segmentCount
	as.mu.Unlock()

	if !last {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// Final segment: verify the whole reassembled file before exposing it.
	hf, err := os.Open(partialPath)
	if err != nil {
		s.failSegment(as, key, partialPath, subID, fname, err.Error())
		http.Error(w, "cannot open assembled file", 500)
		return
	}
	h := sha256.New()
	if _, err := io.Copy(h, hf); err != nil {
		_ = hf.Close()
		s.failSegment(as, key, partialPath, subID, fname, err.Error())
		http.Error(w, "hash error", 500)
		return
	}
	_ = hf.Close()
	sum := hex.EncodeToString(h.Sum(nil))
	if expected != "" && !strings.EqualFold(sum, expected) {
		_ = os.Remove(partialPath)
		s.assemblies.Delete(key)
		s.failTransfer(subID, fname, "checksum mismatch")
		http.Error(w, "checksum mismatch", 400)
		return
	}
	if err := os.Rename(partialPath, as.finalPath); err != nil {
		s.failSegment(as, key, partialPath, subID, fname, err.Error())
		http.Error(w, "finalize error", 500)
		return
	}
	s.assemblies.Delete(key)
	s.manager.Complete(subID, as.finalPath, sum)
	dest := s.settings.GetSettings().DownloadDir
	if s.app != nil {
		s.app.Event.Emit("transfer-complete", map[string]any{
			"id": subID, "filename": fname, "size": size, "filePath": as.finalPath, "checksum": sum,
			"destination": dest, "destinationUri": s.settings.GetSettings().DownloadDirUri,
			"senderId": senderID, "senderAddr": senderAddr, "senderType": senderType,
		})
	}
	w.WriteHeader(200)
	_, _ = fmt.Fprintf(w, "ok %s", sum)
}

func (s *FileTransferService) failSegment(as *assemblyState, key, partialPath, subID, fname, msg string) {
	as.mu.Lock()
	as.failed = true
	as.mu.Unlock()
	s.assemblies.Delete(key)
	_ = os.Remove(partialPath)
	s.failTransfer(subID, fname, msg)
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
			atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusPaused)
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
			atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusActive)
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
		atomic.StoreInt32(&ctrl.statusAtomic, ctrlStatusCancelled)
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
	total    *int64 // optional shared total across parallel segments
	app      *application.App
	manager  *TransferManager
}

func (c *countingReader) Read(p []byte) (int, error) {
	for {
		switch atomic.LoadInt32(&c.ctrl.statusAtomic) {
		case ctrlStatusActive:
			n, err := c.r.Read(p)
			if n > 0 {
				if c.total != nil {
					atomic.AddInt64(c.total, int64(n))
				}
				c.sent += int64(n)
				c.reportProgress(false)
			}
			return n, err
		case ctrlStatusPaused:
			c.ctrl.mu.Lock()
			ch := c.ctrl.resumeCh
			c.ctrl.mu.Unlock()
			<-ch
		case ctrlStatusCancelled:
			return 0, context.Canceled
		}
	}
}

func (c *countingReader) reportProgress(force bool) {
	now := time.Now()
	if !force && now.Sub(c.lastEmit) < progressInterval {
		return
	}
	c.lastEmit = now
	transferred := c.sent
	if c.total != nil {
		transferred = atomic.LoadInt64(c.total)
	}
	elapsed := now.Sub(c.started).Seconds()
	speed := int64(0)
	if elapsed > 0 {
		speed = int64(float64(transferred) / elapsed)
	}
	c.manager.UpdateProgress(c.subID, transferred, speed)
	if c.app != nil {
		c.app.Event.Emit("transfer-progress", map[string]any{
			"id": c.subID, "filename": c.fname, "transferred": c.sent, "size": c.size, "speed": speed,
		})
	}
}

type receivingWriter struct {
	dst      io.Writer
	subID    string
	fname    string
	size     int64
	offset   int64
	written  int64
	started  time.Time
	lastEmit time.Time
	app      *application.App
	manager  *TransferManager
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
	if !force && now.Sub(w.lastEmit) < progressInterval {
		return
	}
	w.lastEmit = now
	elapsed := now.Sub(w.started).Seconds()
	speed := int64(0)
	if elapsed > 0 {
		speed = int64(float64(w.written) / elapsed)
	}
	transferred := w.offset + w.written
	w.manager.UpdateProgress(w.subID, transferred, speed)
	if w.app != nil {
		w.app.Event.Emit("transfer-progress", map[string]any{
			"id": w.subID, "filename": w.fname, "transferred": transferred, "size": w.size, "speed": speed,
		})
	}
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
