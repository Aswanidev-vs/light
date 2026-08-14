//go:build darwin

package light

import "context"

// macOS has no public raw Wi-Fi Direct API; MultipeerConnectivity does not expose
// an IP:port that the existing HTTP transfer server can bind to. The toggle is
// hidden in the UI, so this path is only reached defensively.
type darwinWifiDirectManager struct{}

func (darwinWifiDirectManager) Discover(context.Context) ([]WifiDirectPeer, error) {
	return nil, ErrWifiDirectUnsupported
}

func (darwinWifiDirectManager) Connect(context.Context, string) (string, error) {
	return "", ErrWifiDirectUnsupported
}

func (darwinWifiDirectManager) Close() error { return nil }

func newPlatformWifiDirectManager() (WifiDirectManager, error) {
	return darwinWifiDirectManager{}, nil
}
