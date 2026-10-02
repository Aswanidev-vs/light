package light

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestParsePickPath(t *testing.T) {
	p, ok := parsePickPath("lightpick://127.0.0.1:39481/9c1d4e/a1b2/IMG_0042.jpg")
	if !ok {
		t.Fatal("expected a valid pick path")
	}
	if p.host != "127.0.0.1:39481" || p.raw != "9c1d4e/a1b2/IMG_0042.jpg" || p.name != "IMG_0042.jpg" {
		t.Fatalf("got host=%q raw=%q name=%q", p.host, p.raw, p.name)
	}
}

// The raw path must be replayed byte-for-byte: re-encoding it here would escape
// the name's own separators into %2F and break the read server's parsing.
func TestParsePickPathKeepsRawPath(t *testing.T) {
	p, ok := parsePickPath("lightpick://h:1/tok/id/My%20Video.mp4")
	if !ok {
		t.Fatal("expected a valid pick path")
	}
	if got := pickURLFor(p.host, p.raw); got != "http://h:1/tok/id/My%20Video.mp4" {
		t.Fatalf("replayed URL = %q", got)
	}
}

func TestParsePickPathEscapedName(t *testing.T) {
	// Media filenames routinely contain spaces and '#'/'?', all of which are
	// URL-significant and must survive the round trip.
	cases := map[string]string{
		"My%20Video.mp4":        "My Video.mp4",
		"clip%23one%3F.mkv":     "clip#one?.mkv",
		"a%2Bb%26c.png":         "a+b&c.png",
		"100%25%20done%2Epdf":   "100% done.pdf",
		"unicode%E2%9C%93%20ok": "unicode✓ ok",
		"plain_under-score.txt": "plain_under-score.txt",
		"dots.in.name.tar.gz":   "dots.in.name.tar.gz",
	}
	for encoded, want := range cases {
		p, ok := parsePickPath("lightpick://h:1/tok/id/" + encoded)
		if !ok {
			t.Fatalf("%q: expected valid path", encoded)
		}
		if p.name != want {
			t.Errorf("%q: name = %q, want %q", encoded, p.name, want)
		}
	}
}

func TestParsePickPathRejectsNonPickPaths(t *testing.T) {
	bad := []string{
		"",
		"C:/Users/me/clip.mp4",
		"/tmp/clip.mp4",
		"lightpick://",
		"lightpick://host:1",
		"lightpick://host:1/token",
		"lightpick://host:1/token/id",
		"lightpick://host:1/token/",
		"lightpick://host:1/token/id/",
		"lightpick:///token/id/name",
		"lightpick://host:1//id/name",
	}
	for _, p := range bad {
		if _, ok := parsePickPath(p); ok {
			t.Errorf("%q: expected parse to fail", p)
		}
	}
}

func TestSendSourceName(t *testing.T) {
	if got := sendSourceName("lightpick://127.0.0.1:1/tok/id/My%20Video.mp4"); got != "My Video.mp4" {
		t.Errorf("pick source name = %q", got)
	}
	if got := sendSourceName(filepath.Join("a", "b", "plain.mp4")); got != "plain.mp4" {
		t.Errorf("file source name = %q", got)
	}
}

func TestStatSendSourceFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 1234), 0o644); err != nil {
		t.Fatal(err)
	}
	name, size, err := statSendSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "plain.bin" || size != 1234 {
		t.Fatalf("name=%q size=%d", name, size)
	}
}

// pickRangeServer stands in for the Android read server: it serves a fixed
// payload over HTTP with correct single-range semantics.
func pickRangeServer(t *testing.T, payload []byte, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			*calls++
		}
		cr := r.Header.Get("Range")
		if !strings.HasPrefix(cr, "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Write(payload)
			return
		}
		var start, end int
		part := strings.TrimPrefix(cr, "bytes=")
		lo, hi, _ := strings.Cut(part, "-")
		start, _ = strconv.Atoi(lo)
		end, _ = strconv.Atoi(hi)
		if end >= len(payload) {
			end = len(payload) - 1
		}
		if start > end {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(payload[start : end+1])
	}))
}

func TestPickReaderAtReadAtFillsExactly(t *testing.T) {
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i)
	}
	srv := pickRangeServer(t, payload, nil)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	if src.Size() != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", src.Size(), len(payload))
	}

	// ReadAt must fill the buffer exactly or return an error; a short read here
	// would hand the segmented uploader the wrong bytes silently.
	buf := make([]byte, 1000)
	n, err := src.ReadAt(buf, 0)
	if err != nil || n != len(buf) {
		t.Fatalf("ReadAt(0) = %d, %v", n, err)
	}
	if !bytes.Equal(buf, payload[:1000]) {
		t.Fatal("ReadAt(0) returned wrong bytes")
	}

	n, err = src.ReadAt(buf, 2048)
	if err != nil || n != len(buf) {
		t.Fatalf("ReadAt(2048) = %d, %v", n, err)
	}
	if !bytes.Equal(buf, payload[2048:3048]) {
		t.Fatal("ReadAt(2048) returned wrong bytes")
	}
}

func TestPickReaderAtClampsToFileEnd(t *testing.T) {
	payload := []byte("0123456789")
	srv := pickRangeServer(t, payload, nil)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	// A read that runs past the end must return only the tail bytes.
	buf := make([]byte, 4)
	n, err := src.ReadAt(buf, 8)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if string(buf[:n]) != "89" {
		t.Fatalf("tail = %q, want %q", buf[:n], "89")
	}
}

func TestPickReaderAtEOFAtAndPastEnd(t *testing.T) {
	srv := pickRangeServer(t, []byte("0123456789"), nil)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadAt(make([]byte, 4), 10); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt at EOF = %v, want io.EOF", err)
	}
	if _, err := src.ReadAt(make([]byte, 4), 500); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt past EOF = %v, want io.EOF", err)
	}
}

// TestPickSourceAsSectionReader exercises the exact shape the segmented uploader
// uses: io.NewSectionReader over the source, covering the whole file in the same
// segment layout segmentCount would produce.
func TestPickSourceAsSectionReader(t *testing.T) {
	payload := make([]byte, 3000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	srv := pickRangeServer(t, payload, nil)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/file.bin")
	if err != nil {
		t.Fatal(err)
	}

	const segs = 4
	seg := int64(len(payload)) / segs
	var got []byte
	for i := 0; i < segs; i++ {
		start := int64(i) * seg
		end := start + seg
		if i == segs-1 {
			end = int64(len(payload))
		}
		chunk, err := io.ReadAll(io.NewSectionReader(src, start, end-start))
		if err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		got = append(got, chunk...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("reassembled segments did not match the source")
	}
}

// TestPickSourceSequentialReader covers the background SHA-256 path, which walks
// the source end to end and must terminate cleanly on the final short read.
func TestPickSourceSequentialReader(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 5000)
	srv := pickRangeServer(t, payload, nil)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	r, err := src.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("sequential read ended with %v, want a clean EOF", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("sequential read returned wrong bytes")
	}
}

func TestOpenPickSourceUnsupportedOffAndroid(t *testing.T) {
	// Non-Android builds have no read server, so the prefix must fail loudly
	// rather than being mistaken for a missing file.
	if _, err := openSendSource("lightpick://127.0.0.1:1/tok/id/name.mp4"); !errors.Is(err, errPickSourceUnsupported) {
		t.Fatalf("err = %v, want errPickSourceUnsupported", err)
	}
}

func TestOpenSendSourceFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.bin")
	payload := []byte("hello world")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := openSendSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.Size() != int64(len(payload)) {
		t.Fatalf("size = %d", src.Size())
	}
	second, err := src.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got, err := io.ReadAll(second)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("second handle = %q, %v", got, err)
	}
}

// TestPickReaderAtCollapsesSmallReads is the regression guard for the request
// fan-out problem: net/http copies request bodies in 32 KiB chunks, so without
// the window cache a large upload would issue one ranged GET per chunk.
func TestPickReaderAtCollapsesSmallReads(t *testing.T) {
	payload := make([]byte, pickWindowSize+pickWindowSize/2)
	for i := range payload {
		payload[i] = byte(i % 253)
	}
	var requests int
	srv := pickRangeServer(t, payload, &requests)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/id/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	// Walk the whole file the way a sequential reader would, in 32 KiB chunks.
	r, err := src.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}

	// One probe plus one fetch per window, not one per 32 KiB chunk.
	maxExpected := 2 + int((int64(len(payload))+pickWindowSize-1)/pickWindowSize)
	if requests > maxExpected {
		t.Fatalf("sequential read issued %d HTTP requests for %d bytes; expected at most %d",
			requests, len(payload), maxExpected)
	}
	t.Logf("%d bytes read via %d HTTP requests", len(payload), requests)
}

// TestPickReaderAtConcurrentReadsShareWindows makes sure the ring stays correct
// when several segments read different offsets at once.
func TestPickReaderAtConcurrentReadsShareWindows(t *testing.T) {
	payload := make([]byte, pickWindowSize*3)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	srv := pickRangeServer(t, payload, nil)
	defer srv.Close()

	src, err := newPickReaderAt(srv.URL + "/tok/id/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the same file-sized payload for every reader.
	src.size = int64(len(payload))

	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			off := int64(g) * pickWindowSize
			for i := 0; i < 16; i++ {
				buf := make([]byte, 4096)
				n, err := src.ReadAt(buf, off+int64(i)*4096)
				if err != nil {
					t.Errorf("group %d read %d: %v", g, i, err)
					return
				}
				if !bytes.Equal(buf, payload[off+int64(i)*4096:off+int64(i)*4096+4096]) {
					t.Errorf("group %d read %d returned wrong bytes", g, i)
					return
				}
				_ = n
			}
		}(g)
	}
	wg.Wait()
}
