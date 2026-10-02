package light

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pickHTTPClient talks to the Android picker read server on loopback. Proxies are
// explicitly disabled: the host is 127.0.0.1 and a proxy env var must never be
// allowed to redirect a local byte stream off-device. MaxConnsPerHost is capped
// to match the server's worker pool so a burst of segment reads queues instead
// of piling up half-open sockets.
var pickHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:               nil,
		DisableCompression:  true,
		MaxConnsPerHost:     pickWindowSlots,
		MaxIdleConnsPerHost: pickWindowSlots,
	},
	// No client timeout: a single window fetch can legitimately span a slow
	// content provider. Cancellation is driven by the caller's context instead.
	Timeout: 0,
}

// The uploader hands us a plain io.Reader, and net/http copies request bodies in
// 32 KiB chunks. Without a window cache every one of those chunks would become
// its own ranged GET, so a 1 GiB upload would open ~32k HTTP requests. Instead a
// read pulls whole window-sized blocks and serves sub-reads out of memory.
const (
	pickWindowSize  = 4 << 20 // 4 MiB
	pickWindowSlots = 6       // at most ~24 MiB resident
)

// pickWindow is one cached block of the source file.
type pickWindow struct {
	index int64 // block number, offset/pickWindowSize
	seq   int64 // insertion order, for eviction
	data  []byte
}

// pickReaderAt serves one picker-backed file as an io.ReaderAt over loopback
// HTTP, backed by a small ring of window buffers. ReadAt is safe for concurrent
// use because several upload segments read different offsets at once.
type pickReaderAt struct {
	url  string
	size int64

	mu  sync.Mutex
	win []pickWindow
	seq int64 // round-robin eviction counter
}

func newPickReaderAt(rawURL string) (*pickReaderAt, error) {
	size, err := probePickSize(rawURL)
	if err != nil {
		return nil, err
	}
	return &pickReaderAt{url: rawURL, size: size}, nil
}

func (p *pickReaderAt) Size() int64  { return p.size }
func (p *pickReaderAt) Close() error { return nil }

func (p *pickReaderAt) Open() (io.ReadCloser, error) {
	return &pickSeqReader{p: p}, nil
}

// ReadAt fills p from the given absolute offset. io.SectionReader depends on this
// returning either the full length or an error, so a short read is surfaced as
// io.ErrUnexpectedEOF rather than being quietly accepted.
func (p *pickReaderAt) ReadAt(b []byte, off int64) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, errors.New("picksource: negative offset")
	}
	if off >= p.size {
		return 0, io.EOF
	}
	end := off + int64(len(b)) - 1
	if end >= p.size {
		end = p.size - 1
	}
	want := int(end - off + 1)

	filled := 0
	for filled < want {
		pos := off + int64(filled)
		window, err := p.windowAt(pos)
		if err != nil {
			if filled > 0 {
				return filled, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		start := pos - window.index*pickWindowSize
		n := copy(b[filled:filled+int(end-pos+1)], window.data[start:])
		if n == 0 {
			break
		}
		filled += n
	}

	if filled < want {
		return filled, io.ErrUnexpectedEOF
	}
	if want < len(b) {
		// The read ran past end-of-file. Report the short read: io.ReaderAt
		// requires a non-nil error whenever fewer than len(b) bytes came back.
		return filled, io.ErrUnexpectedEOF
	}
	return filled, nil
}

// windowAt returns the cached block containing off, fetching it on a miss.
func (p *pickReaderAt) windowAt(off int64) (*pickWindow, error) {
	index := off / pickWindowSize
	if w := p.cached(index); w != nil {
		return w, nil
	}

	start := index * pickWindowSize
	length := p.size - start
	if length > pickWindowSize {
		length = pickWindowSize
	}
	data, err := p.fetchWindow(start, length)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// Another segment may have cached the same block while we were fetching.
	entry := pickWindow{index: index, seq: p.seq, data: data}
	p.seq++
	if len(p.win) < pickWindowSlots {
		p.win = append(p.win, entry)
	} else {
		oldest := 0
		for i := range p.win {
			if p.win[i].seq < p.win[oldest].seq {
				oldest = i
			}
		}
		p.win[oldest] = entry
	}
	for i := range p.win {
		if p.win[i].index == index {
			return &p.win[i], nil
		}
	}
	return nil, errors.New("picksource: window cache insert failed")
}

// cached returns the resident block with the given index, or nil.
func (p *pickReaderAt) cached(index int64) *pickWindow {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.win {
		if p.win[i].index == index {
			return &p.win[i]
		}
	}
	return nil
}

// fetchWindow pulls one block with a single ranged GET.
func (p *pickReaderAt) fetchWindow(start, length int64) ([]byte, error) {
	end := start + length - 1
	resp, err := pickGet(context.Background(), p.url, fmt.Sprintf("bytes=%d-%d", start, end))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("picksource: unexpected status %d for bytes %d-%d", resp.StatusCode, start, end)
	}
	data := make([]byte, length)
	n, err := io.ReadFull(resp.Body, data)
	if err != nil && n == 0 {
		return nil, fmt.Errorf("picksource: short window read at %d: %w", start, err)
	}
	return data[:n], nil
}

// pickSeqReader walks a pickReaderAt sequentially, for the background integrity
// hash.
type pickSeqReader struct {
	p   *pickReaderAt
	off int64
}

func (r *pickSeqReader) Read(b []byte) (int, error) {
	if r.off >= r.p.size {
		return 0, io.EOF
	}
	n, err := r.p.ReadAt(b, r.off)
	r.off += int64(n)
	// ReadAt reports a truncated tail as ErrUnexpectedEOF; for a sequential walk
	// that is simply the end of the file.
	if errors.Is(err, io.ErrUnexpectedEOF) && r.off >= r.p.size {
		return n, io.EOF
	}
	return n, err
}

func (r *pickSeqReader) Close() error { return nil }

// probePickSize resolves the real file length with a one-byte ranged request and
// reads it back out of the Content-Range total. Reusing the byte-range endpoint
// means the same access token authenticates the metadata, so no separate JSON
// status route is needed.
func probePickSize(rawURL string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pickProbeTimeout)
	defer cancel()
	resp, err := pickGet(ctx, rawURL, "bytes=0-0")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	total := resp.Header.Get("Content-Range")
	if i := strings.LastIndexByte(total, '/'); i >= 0 {
		total = total[i+1:]
	}
	size, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("picksource: bad Content-Range %q", resp.Header.Get("Content-Range"))
	}
	return size, nil
}

func pickGet(ctx context.Context, rawURL, rangeSpec string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", rangeSpec)
	resp, err := pickHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("picksource: %w", err)
	}
	return resp, nil
}

// pickURLFor renders the loopback URL a pickReaderAt reads from. The path is
// replayed exactly as the read server registered it.
func pickURLFor(host, rawPath string) string {
	return "http://" + host + "/" + rawPath
}

// pickProbeTimeout bounds the size probe so a dead read server fails the send
// quickly instead of hanging the picker flow. Actual byte reads stay unbounded.
const pickProbeTimeout = 5 * time.Second
