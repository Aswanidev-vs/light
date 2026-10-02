package light

import (
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// pickScheme marks a send-side path that is served by the platform picker read
// server instead of the local filesystem. The path is self-describing so it can
// travel through the existing TransferRequest.FilePaths string slice unchanged:
//
//	lightpick://127.0.0.1:39481/<token>/<name>
//
// The trailing name is required, not decorative: filepath.Base of this string is
// what the sender puts on the wire as X-Filename, so a token-only path would
// hand the receiver a garbage filename.
const pickScheme = "lightpick://"

// errPickSourceUnsupported is returned off Android, where no platform read server
// exists. A lightpick:// path there is a caller bug, not a filesystem miss.
var errPickSourceUnsupported = errors.New("light: picker-backed source is only supported on android")

// sendSource is the minimal surface the uploader needs: a seekable body plus its
// length, so io.NewSectionReader can carve parallel segments out of it. ReadAt
// must be safe for concurrent use, since a segmented file drives several
// goroutines against one source.
type sendSource interface {
	io.ReaderAt
	io.Closer
	Size() int64
	// Open returns a second, independent sequential reader over the same bytes so
	// the background integrity hash can run alongside the upload instead of
	// delaying its first byte.
	Open() (io.ReadCloser, error)
}

// fileSendSource adapts an ordinary file to sendSource.
type fileSendSource struct {
	f    *os.File
	size int64
}

func (s *fileSendSource) ReadAt(p []byte, off int64) (int, error) { return s.f.ReadAt(p, off) }
func (s *fileSendSource) Close() error                            { return s.f.Close() }
func (s *fileSendSource) Size() int64                             { return s.size }

func (s *fileSendSource) Open() (io.ReadCloser, error) { return os.Open(s.f.Name()) }

// pickPathParts is a parsed lightpick:// path.
type pickPathParts struct {
	// host is the loopback authority, "127.0.0.1:<port>".
	host string
	// raw is the path remainder exactly as the producer wrote it, already
	// percent-encoded. It is replayed verbatim into the HTTP request so the read
	// server sees the very segments it registered; re-encoding here would turn
	// the name's own separators into %2F and break that server's parsing.
	raw string
	// name is the decoded display filename.
	name string
}

// parsePickPath splits a lightpick:// path into its loopback authority, the raw
// (still-encoded) path, and the decoded display name. The path layout mirrors
// what the read server registers: <token>/<id>/<encoded name>.
func parsePickPath(path string) (pickPathParts, bool) {
	if !strings.HasPrefix(path, pickScheme) {
		return pickPathParts{}, false
	}
	rest := path[len(pickScheme):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return pickPathParts{}, false
	}
	host, raw := rest[:slash], rest[slash+1:]

	slash = strings.IndexByte(raw, '/')
	if slash < 0 {
		return pickPathParts{}, false
	}
	token, tail := raw[:slash], raw[slash+1:]
	slash = strings.IndexByte(tail, '/')
	if slash < 0 {
		return pickPathParts{}, false
	}
	id, encoded := tail[:slash], tail[slash+1:]
	if host == "" || token == "" || id == "" || encoded == "" {
		return pickPathParts{}, false
	}
	name, err := url.PathUnescape(encoded)
	if err != nil {
		return pickPathParts{}, false
	}
	return pickPathParts{host: host, raw: raw, name: name}, true
}

// sendSourceName returns the filename a send-side path should be presented and
// transmitted under.
func sendSourceName(path string) string {
	if p, ok := parsePickPath(path); ok {
		return p.name
	}
	return filepath.Base(path)
}

// statSendSource reports the name and size of a send-side path without reading
// its contents. Picker sources resolve their size from the read server.
func statSendSource(path string) (string, int64, error) {
	if _, ok := parsePickPath(path); !ok {
		info, err := os.Stat(path)
		if err != nil {
			return "", 0, err
		}
		return filepath.Base(path), info.Size(), nil
	}
	ss, err := openSendSource(path)
	if err != nil {
		return "", 0, err
	}
	defer ss.Close()
	return sendSourceName(path), ss.Size(), nil
}

// openSendSource resolves a TransferRequest path to something the uploader can
// read from. Filesystem paths open directly; lightpick:// paths are served by
// the platform read server.
func openSendSource(path string) (sendSource, error) {
	if _, ok := parsePickPath(path); !ok {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		return &fileSendSource{f: f, size: info.Size()}, nil
	}
	return openPickSource(path)
}
