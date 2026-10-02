//go:build !android

package light

// openPickSource has no counterpart off Android, where nothing serves picker
// sources. Desktop paths are ordinary files and never reach this function.
func openPickSource(string) (sendSource, error) {
	return nil, errPickSourceUnsupported
}
