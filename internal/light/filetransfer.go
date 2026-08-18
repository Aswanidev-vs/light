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
	// A single large file is split into this many parallel ranges, independent
	// of the file-level upload cap, so one 1 GB file can saturate several
	// streams on lossy Wi-Fi and higher-latency links. Mobile uses fewer ranges
	// to bound CPU and disk pressure.
	maxSegmentsPerFile    = 8
	mobileSegmentsPerFile = 4
)

var transferBufferPool = sync.Pool{
	New: func() any { return make([]byte, chunkSize) },
}

// rateTracker derives the CURRENT transfer rate from samples taken at each
// progress emit. A lifetime average (total/elapsed) is what made the reported
// speed look like it "kept dropping" mid-transfer; this instead measures the
// bytes moved since the previous emit (typically one progressInterval window)
// and smooths it with the prior rate so one slow window doesn't whipsaw the UI.
//
// sample is mutex-guarded because a segmented upload shares one tracker across
// its concurrent segment streams.
type rateTracker struct {
	mu       sync.Mutex
	started  time.Time
	prevRead int64
	prevTime time.Time
	rate     int64
	primed   bool
}

// sample registers the cumulative byte count at now and returns the smoothed
// current rate in bytes/second.
func (rt *rateTracker) sample(now time.Time, read int64) int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.primed {
		rt.primed = true
		rt.prevRead, rt.prevTime = read, now
		if elapsed := now.Sub(rt.started).Seconds(); elapsed > 0 {
			rt.rate = int64(float64(read) / elapsed)
		}
		return rt.rate
	}
	if dt := now.Sub(rt.prevTime).Seconds(); dt > 0 {
		inst := int64(float64(read-rt.prevRead) / dt)
		if inst < 0 {
			inst = 0
		}
		rt.rate = (rt.rate + inst) / 2
	}
	rt.prevRead, rt.prevTime = read, now
	return rt.rate
}

// errTransferCancelled aborts an inbound copy when the receiving user cancels
// the transfer; the handler turns it into a clean cancelled finalization.
var errTransferCancelled = errors.New("transfer cancelled")

// errReceiverCancelled is what an upload returns when the peer answered with a
// cancellation instead of accepting the data.
var errReceiverCancelled = errors.New("receiver cancelled")

// cancellationReader aborts an inbound body read once the receiver-side cancel
// flag is set. Checked once per chunk (1 MiB), so a cancelled inbound transfer
// stops streaming within ~1 chunk of the click.
type cancellationReader struct {
	r         io.Reader
	cancelled func() bool
}

func (c *cancellationReader) Read(p []byte) (int, error) {
	if c.cancelled() {
		return 0, errTransferCancelled
	}
	return c.r.Read(p)
}

type acceptState struct {
	status     string // pending | accepted | rejected | cancelled
	senderID   string
	senderAddr string
	senderType DeviceType
	// files lists the filenames from the prepare payload so a receive-side
	// cancel can finalize every row of the batch, not just the clicked one.
	files []string
	// cancelled is set by CancelTransfer on the receiving device; inbound
	// readers observe it and abort mid-stream. Atomic so hot-path reads need
	// no lock.
	cancelled atomic.Bool
}

// isCancelled returns a lock-free predicate the inbound readers poll once per
// chunk.
func (st *acceptState) isCancelled() bool {
	return st != nil && st.cancelled.Load()
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
	mu            sync.Mutex
	totalSize     int64
	segmentCount  int
	received      int
	receivedBytes int64 // total bytes received across all segments (atomic)
	partialPath   string
	finalPath     string
	checksum      string
	// segmentDigests holds the receiver's in-stream SHA-256 of each landed
	// segment; declaredDigests holds the sender's per-segment digest from the
	// X-Segment-Digest header ("" when the sender did not provide one). When
	// every segment arrived with a matching declared digest, the last segment
	// skips the whole-file read-back hash before renaming into place.
	segmentDigests  []string
	declaredDigests []string
	failed          bool
	canceled        bool
	started         time.Time
	lastEmit        time.Time // guarded by mu
	rate            rateTracker
}

// receiveCounter wraps the inbound request body on the receiver so a segmented
// (parallel-range) upload still reports live progress. Bytes from every
// concurrent segment accumulate into the shared assemblyState.receivedBytes,
// giving one monotonic "transferred" total for the whole file.
type receiveCounter struct {
	r       io.Reader
	as      *assemblyState
	subID   string
	fname   string
	emitFn  func(string, any)
	manager *TransferManager
}

func (rc *receiveCounter) Read(p []byte) (int, error) {
	n, err := rc.r.Read(p)
	if n > 0 {
		atomic.AddInt64(&rc.as.receivedBytes, int64(n))
		rc.as.reportProgress(rc.subID, rc.fname, rc.emitFn, rc.manager, false)
	}
	return n, err
}

func (as *assemblyState) reportProgress(subID, fname string, emitFn func(string, any), manager *TransferManager, force bool) {
	now := time.Now()
	as.mu.Lock()
	if !force && now.Sub(as.lastEmit) < progressInterval {
		as.mu.Unlock()
		return
	}
	as.lastEmit = now
	as.mu.Unlock()
	transferred := atomic.LoadInt64(&as.receivedBytes)
	speed := as.rate.sample(now, transferred)
	manager.UpdateProgress(subID, transferred, speed)
	if emitFn != nil {
		emitFn("transfer-progress", map[string]any{
			"id": subID, "filename": fname, "transferred": transferred, "size": as.totalSize, "speed": speed,
		})
	}
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
	// testEmit, when non-nil, captures emitted events in place of the Wails
	// runtime. Used only by tests; always nil in production.
	testEmit func(name string, payload any)
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
			MaxIdleConns: 32,
			// A 4-file batch × 8 segments per file can push 32 concurrent
			// streams at one peer; a lower per-host cap would queue segments.
			MaxIdleConnsPerHost: 32,
			MaxConnsPerHost:     32,
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
			quicConn, err = (&net.ListenConfig{Control: quicSocketControl}).ListenPacket(context.Background(), "udp", fmt.Sprintf(":%d", port))
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
		names := make([]string, 0, len(p.Files))
		for _, f := range p.Files {
			names = append(names, f.Name)
		}
		st = &acceptState{status: "pending", files: names}
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
		segDigest := r.Header.Get("X-Segment-Digest")
		s.handleSegmentedTransfer(w, r, tid, fname, size, offset, segIndex, segCount, checksum, segDigest, senderID, senderAddr, senderType)
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
	receiver.rate = rateTracker{started: receiver.started}
	buffer := transferBufferPool.Get().([]byte)
	defer transferBufferPool.Put(buffer)
	// The body is wrapped so a receiver-side cancel aborts the copy within one
	// chunk; the partial file is removed by the deferred cleanup above.
	body := &cancellationReader{r: r.Body, cancelled: st.isCancelled}
	written, err := io.CopyBuffer(receiver, body, buffer)
	receiver.reportProgress(true)
	if err != nil {
		if errors.Is(err, errTransferCancelled) {
			// The deferred cleanup removes the partial file; mark the row
			// cancelled and answer 499 so the sender stops instead of retrying.
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
			http.Error(w, "cancelled", 499)
			return
		}
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
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// peerCancelProbe asks the peer whether the transfer was cancelled on the
// receive side. A "cancelled" status answer means the peer stopped accepting
// data voluntarily, so the upload should surface a cancellation rather than a
// generic network failure. Any probe failure (old peer, network down) reports
// false and leaves the error classification to the caller.
func (s *FileTransferService) peerCancelProbe(client *http.Client, scheme, peerAddr, tid string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+peerAddr+"/api/status/"+tid, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode == 200 && strings.TrimSpace(string(b)) == "cancelled"
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

	// Hash the file in the background from a second handle so the SHA-256 cost
	// never delays the first byte on the wire. Segmented uploads no longer
	// declare per-segment digests, so the digest is only needed for the final
	// comparison; the receiver verifies the on-disk bytes itself.
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
	segCount := s.segmentCount(size)
	fileStart := time.Now()

	if segCount <= 1 {
		rc, err := s.uploadRange(ctx, client, scheme, peerAddr, tid, fname, size, 0, size, filePath, subID, ctrl, 0, 1, fileStart, nil)
		if err != nil {
			return err
		}
		return s.verifyAndComplete(subID, fname, size, rc, &senderChecksum, hashDone, &hashErr)
	}

	segSize := size / int64(segCount)
	boundaries := make([]int64, segCount)
	for i := range boundaries {
		boundaries[i] = int64(i+1) * segSize
		if i == segCount-1 {
			boundaries[i] = size
		}
	}

	var totalSent int64
	var wg sync.WaitGroup
	var failuresMu sync.Mutex
	var failures []string
	var checksumMu sync.Mutex
	var receiverChecksum string
	for i := 0; i < segCount; i++ {
		start := int64(0)
		if i > 0 {
			start = boundaries[i-1]
		}
		end := boundaries[i]
		wg.Add(1)
		go func(i int, start, end int64) {
			defer wg.Done()
			rc, err := s.uploadRange(ctx, client, scheme, peerAddr, tid, fname, size, start, end-start, filePath, subID, ctrl, i, segCount, fileStart, &totalSent)
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
	return s.verifyAndComplete(subID, fname, size, receiverChecksum, &senderChecksum, hashDone, &hashErr)
}

// segmentCount returns how many parallel ranges a file is split into for
// upload. Files below the segment threshold stay single-stream; mobile devices
// use fewer ranges to bound CPU and disk pressure there.
func (s *FileTransferService) segmentCount(size int64) int {
	if size < segmentMinSize {
		return 1
	}
	dev := s.deviceType
	if dev == "" {
		dev = PlatformDeviceType()
	}
	count := maxSegmentsPerFile
	if dev == DeviceTypeMobile {
		count = mobileSegmentsPerFile
	}
	if count < 1 {
		count = 1
	}
	return count
}

// uploadRange streams one [offset, offset+length) slice of the file as its own
// HTTP request. For segmented transfers it carries X-Segment-* headers; the
// final segment's response carries the receiver's whole-file checksum, which
// the caller verifies against the sender's.
func (s *FileTransferService) uploadRange(ctx context.Context, client *http.Client, scheme, peerAddr, tid, fname string, size, offset, length int64, filePath, subID string, ctrl *sendControl, segIndex, segCount int, started time.Time, totalSent *int64) (string, error) {
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
		rate:     rateTracker{started: started},
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
	}

	resp, err := client.Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
		case s.peerCancelProbe(client, scheme, peerAddr, tid):
			// The peer stopped accepting (it cancelled the receive); report as
			// cancelled rather than a generic failure.
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
			err = errReceiverCancelled
		default:
			s.failTransfer(subID, fname, err.Error())
		}
		return "", err
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		// A cancellation answering 499 (new receivers) or surfacing as another
		// status (older ones mid-read) both resolve through the status probe.
		if resp.StatusCode == 499 || s.peerCancelProbe(client, scheme, peerAddr, tid) {
			s.manager.Cancel(subID)
			s.emit(map[string]string{"id": subID}, "transfer-cancelled")
			return "", errReceiverCancelled
		}
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
// senderChecksum is a pointer because the background hash goroutine writes it;
// reading a captured value at the call site could observe "" when the network
// copy finishes before the hash does.
func (s *FileTransferService) verifyAndComplete(subID, fname string, size int64, receiverChecksum string, senderChecksum *string, hashDone chan struct{}, hashErr *error) error {
	<-hashDone
	if *hashErr != nil {
		s.failTransfer(subID, fname, (*hashErr).Error())
		return *hashErr
	}
	if receiverChecksum == "" {
		s.failTransfer(subID, fname, "missing receiver checksum")
		return errors.New("missing receiver checksum")
	}
	if !strings.EqualFold(receiverChecksum, *senderChecksum) {
		errMsg := fmt.Sprintf("checksum mismatch: sender %s, receiver %s", *senderChecksum, receiverChecksum)
		s.failTransfer(subID, fname, errMsg)
		return errors.New(errMsg)
	}
	s.manager.Complete(subID, "", *senderChecksum)
	if s.app != nil {
		s.app.Event.Emit("transfer-complete", map[string]any{
			"id": subID, "filename": fname, "size": size, "filePath": "", "checksum": *senderChecksum,
		})
	}
	return nil
}

// handleSegmentedTransfer reassembles a file sent as parallel ranges. Each range
// arrives as its own /api/transfer request carrying X-Segment-* headers; all
// ranges for a (transfer id, filename) write into one partial file at their
// given offset. Ranges carrying a sender-declared digest are hashed in-stream
// and verified as they land; when every declared digest matches the last range
// to land skips the whole-file read-back hash. Otherwise the assembled file is
// hashed once before the rename.
func (s *FileTransferService) handleSegmentedTransfer(w http.ResponseWriter, r *http.Request, tid, fname string, size, offset, segIndex, segCount int64, checksum, segDigest, senderID, senderAddr string, senderType DeviceType) {
	dl, err := s.receiveDir()
	if err != nil {
		http.Error(w, "cannot create destination dir: "+err.Error(), 500)
		return
	}
	s.mu.Lock()
	st := s.accepts[tid]
	s.mu.Unlock()
	key := tid + "\x00" + fname
	now := time.Now()
	val, _ := s.assemblies.LoadOrStore(key, &assemblyState{
		totalSize:       size,
		segmentCount:    int(segCount),
		finalPath:       uniquePath(filepath.Join(dl, sanitize(fname))),
		started:         now,
		rate:            rateTracker{started: now},
		segmentDigests:  make([]string, segCount),
		declaredDigests: make([]string, segCount),
	})
	as := val.(*assemblyState)
	if segIndex < 0 || segIndex >= int64(as.segmentCount) {
		http.Error(w, "segment index out of range", 400)
		return
	}
	as.mu.Lock()
	alreadyDone := as.failed || as.canceled
	if alreadyDone {
		// A sibling already failed or cancelled this assembly. Count the
		// arrival so the last sibling can remove the state; don't touch the
		// partial file.
		as.received++
		last := as.received == as.segmentCount
		wasCanceled := as.canceled
		as.mu.Unlock()
		if last {
			s.assemblies.Delete(key)
		}
		if wasCanceled {
			http.Error(w, "cancelled", 499)
		} else {
			http.Error(w, "transfer failed", 400)
		}
		return
	}
	as.mu.Unlock()
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
	emitFn := func(name string, p any) { s.emit(p, name) }
	rc := &receiveCounter{r: &cancellationReader{r: r.Body, cancelled: st.isCancelled}, as: as, subID: subID, fname: fname, emitFn: emitFn, manager: s.manager}
	// Hash the segment in-stream only when the sender declared a per-segment
	// digest to compare against. New senders omit it, so the copy is write-only
	// and the single on-disk hash at finalize covers verification instead.
	var segHash hash.Hash
	var digestTarget io.Writer = f
	if segDigest != "" {
		segHash = sha256.New()
		digestTarget = io.MultiWriter(f, segHash)
	}
	if _, err := io.CopyBuffer(digestTarget, rc, buffer); err != nil {
		_ = f.Close()
		if errors.Is(err, errTransferCancelled) {
			s.cancelSegment(as, key, partialPath, subID)
			http.Error(w, "cancelled", 499)
			return
		}
		s.failSegment(as, key, partialPath, subID, fname, err.Error())
		http.Error(w, "read/write error", 400)
		return
	}
	as.reportProgress(subID, fname, emitFn, s.manager, true)

	computed := ""
	if segHash != nil {
		computed = hex.EncodeToString(segHash.Sum(nil))
	}
	as.mu.Lock()
	as.segmentDigests[segIndex] = computed
	if segDigest != "" {
		as.declaredDigests[segIndex] = segDigest
	}
	mismatch := segDigest != "" && !strings.EqualFold(computed, segDigest)
	if mismatch {
		// failSegment counts this arrival and fails the whole assembly.
		as.mu.Unlock()
		_ = f.Close()
		s.failSegment(as, key, partialPath, subID, fname, "segment checksum mismatch")
		http.Error(w, "segment checksum mismatch", 400)
		return
	}
	as.received++
	last := as.received == as.segmentCount
	as.mu.Unlock()
	if last {
		// One fsync after the last segment lands is enough: every segment's
		// bytes are in the page cache by then and fsync flushes the file.
		_ = f.Sync()
	}
	_ = f.Close()

	if !last {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// Final segment: verify the reassembled file before exposing it. When the
	// sender declared a digest for every segment and each matched in-stream,
	// the per-segment verification already covers the whole byte range and the
	// declared whole-file checksum can be trusted without re-reading the file.
	sum := expected
	verified := expected != ""
	if verified {
		as.mu.Lock()
		for i := 0; i < as.segmentCount; i++ {
			if as.declaredDigests[i] == "" || !strings.EqualFold(as.segmentDigests[i], as.declaredDigests[i]) {
				verified = false
				break
			}
		}
		as.mu.Unlock()
	}
	if !verified {
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
		sum = hex.EncodeToString(h.Sum(nil))
		if expected != "" && !strings.EqualFold(sum, expected) {
			_ = os.Remove(partialPath)
			s.assemblies.Delete(key)
			s.failTransfer(subID, fname, "checksum mismatch")
			http.Error(w, "checksum mismatch", 400)
			return
		}
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
	// Mark the assembly failed and count this arrival, but keep the state
	// mapped until every sibling has landed: segments still in flight must
	// observe the failure instead of re-registering a fresh assembly. The last
	// arrival removes the state.
	as.mu.Lock()
	as.failed = true
	as.received++
	last := as.received == as.segmentCount
	as.mu.Unlock()
	if last {
		s.assemblies.Delete(key)
	}
	_ = os.Remove(partialPath)
	s.failTransfer(subID, fname, msg)
}

// cancelSegment is the cancellation twin of failSegment: it marks the assembly
// canceled, stops counting it toward completion, and — once every in-flight
// sibling has reported in — removes the state and the partial file, then
// surfaces a cancelled (not failed) transfer to the UI.
func (s *FileTransferService) cancelSegment(as *assemblyState, key, partialPath, subID string) {
	safeRemovePartial := func() {
		dl, err := s.receiveDir()
		if err != nil {
			return
		}
		baseAbs, err := filepath.Abs(dl)
		if err != nil {
			return
		}
		targetAbs, err := filepath.Abs(partialPath)
		if err != nil {
			return
		}
		base := filepath.Clean(baseAbs)
		target := filepath.Clean(targetAbs)
		baseWithSep := base
		if !strings.HasSuffix(baseWithSep, string(os.PathSeparator)) {
			baseWithSep += string(os.PathSeparator)
		}
		if target == base || strings.HasPrefix(target, baseWithSep) {
			_ = os.Remove(target)
		}
	}

	as.mu.Lock()
	if as.canceled {
		// A sibling already initiated the cancel; just count ourselves.
		as.received++
		last := as.received == as.segmentCount
		as.mu.Unlock()
		if last {
			s.assemblies.Delete(key)
			safeRemovePartial()
		}
		return
	}
	as.canceled = true
	as.received++
	last := as.received == as.segmentCount
	as.mu.Unlock()
	// The first canceling segment removes the partial file immediately so a
	// partial artifact never lingers; later siblings see canceled and skip.
	safeRemovePartial()
	if last {
		s.assemblies.Delete(key)
	}
	s.manager.Cancel(subID)
	s.emit(map[string]string{"id": subID}, "transfer-cancelled")
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
		s.manager.Cancel(id)
		s.emit(map[string]string{"id": id}, "transfer-cancelled")
		return
	}
	// No sendControl for this id: it is an INBOUND transfer on this device.
	// Flag the acceptState so the inbound readers abort within one chunk, and
	// cancel every file row of the batch immediately so the UI does not wait
	// for each stream to notice.
	s.cancelInbound(id)
}

// cancelInbound cancels a transfer this device is RECEIVING. id is a subID
// (tid:filename); the whole batch is cancelled. The inbound handlers observe
// the acceptState flag mid-stream, remove the partial file, and finalize the
// row as cancelled; this function also surfaces the cancellation right away so
// the receiving UI stops showing "Receiving" without waiting for the network
// round-trip to fail each stream.
func (s *FileTransferService) cancelInbound(id string) {
	tid := id
	if i := strings.Index(id, ":"); i > 0 {
		tid = id[:i]
	}
	s.mu.Lock()
	st := s.accepts[tid]
	var files []string
	if st != nil {
		st.cancelled.Store(true)
		st.status = "cancelled"
		files = append([]string{}, st.files...)
	}
	s.mu.Unlock()
	for _, fname := range files {
		subID := tid + ":" + fname
		s.manager.Cancel(subID)
		s.emit(map[string]string{"id": subID}, "transfer-cancelled")
	}
	if len(files) == 0 && id != "" {
		s.manager.Cancel(id)
		s.emit(map[string]string{"id": id}, "transfer-cancelled")
	}
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
	if s.testEmit != nil {
		s.testEmit(name, payload)
		return
	}
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
	rate     rateTracker
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
	speed := c.rate.sample(now, transferred)
	c.manager.UpdateProgress(c.subID, transferred, speed)
	if c.app != nil {
		c.app.Event.Emit("transfer-progress", map[string]any{
			"id": c.subID, "filename": c.fname, "transferred": transferred, "size": c.size, "speed": speed,
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
	rate     rateTracker
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
	speed := w.rate.sample(now, w.written)
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
	// Normalize separators first so Base can reliably collapse path components
	// across platforms and mixed input.
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "" {
		return "unnamed"
	}

	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "unnamed"
	}
	return out
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
