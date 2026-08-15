package light

import (
	"context"
	"errors"
)

// WifiDirectPeer is a nearby device found via Wi-Fi Direct discovery.
type WifiDirectPeer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WifiDirectManager establishes a Wi-Fi Direct (P2P) link to a peer and reports
// the peer's transfer address. The existing HTTP/TCP (and optional QUIC) transfer
// stack rides on top of the link unchanged — this manager only changes how peers
// become reachable, not the transport protocol.
//
// Implementations are platform-specific and selected at runtime by
// NewWifiDirectManager. On unsupported platforms (e.g. macOS) Connect/Discover
// return ErrWifiDirectUnsupported so callers fall back to LAN discovery.
type WifiDirectManager interface {
	// Discover returns nearby Wi-Fi Direct peers.
	Discover(ctx context.Context) ([]WifiDirectPeer, error)
	// Connect forms a P2P group with the given peer and returns its transfer
	// address as host:port for the existing transfer stack to use.
	Connect(ctx context.Context, peerID string) (string, error)
	// Close tears down any active group and releases resources.
	Close() error
}

// ErrWifiDirectUnsupported is returned by platforms that cannot host a raw
// Wi-Fi Direct link (notably macOS, which only exposes MultipeerConnectivity).
var ErrWifiDirectUnsupported = errors.New("wifi-direct: not supported on this platform")

// ErrWifiDirectGroupOwner is returned when group formation succeeded but this
// device became the group owner: the peer's address is only learned when the
// peer connects to us, so the transfer works the other direction around. Once
// the group is up, normal LAN discovery (UDP beacons) flows across the P2P
// link too, so the peer shows up in the regular device list.
var ErrWifiDirectGroupOwner = errors.New("wifi-direct: this device became the group owner; start the transfer from the other device")

// NewWifiDirectManager returns a platform manager when WifiDirect is enabled, or
// nil when disabled / unsupported so callers fall back to LAN discovery.
func NewWifiDirectManager(enabled bool) (WifiDirectManager, error) {
	if !enabled {
		return nil, nil
	}
	return newPlatformWifiDirectManager()
}
