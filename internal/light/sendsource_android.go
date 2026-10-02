//go:build android

package light

// openPickSource resolves a lightpick:// path against the Android picker read
// server. The host and token are carried in the path itself, so no JNI round
// trip is needed to discover where the server is listening.
func openPickSource(path string) (sendSource, error) {
	p, ok := parsePickPath(path)
	if !ok {
		return nil, errPickSourceUnsupported
	}
	return newPickReaderAt("http://" + p.host + "/" + p.raw)
}
