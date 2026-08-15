package light

import (
	"context"
	"testing"
)

// fakeWifiDirectManager is an in-memory WifiDirectManager for tests; it never
// touches hardware.
type fakeWifiDirectManager struct {
	connectAddr string
	connectErr  error
}

func (f *fakeWifiDirectManager) Discover(ctx context.Context) ([]WifiDirectPeer, error) {
	return nil, nil
}

func (f *fakeWifiDirectManager) Connect(ctx context.Context, peerID string) (string, error) {
	return f.connectAddr, f.connectErr
}

func (f *fakeWifiDirectManager) Close() error { return nil }

func TestNewWifiDirectManagerDisabled(t *testing.T) {
	m, err := NewWifiDirectManager(false)
	if m != nil || err != nil {
		t.Fatalf("NewWifiDirectManager(false) = (%v, %v), want (nil, nil)", m, err)
	}
}

func TestConnectWifiDirectUnsupported(t *testing.T) {
	ds := NewDiscoveryService(nil, &SettingsService{cfg: Settings{}})
	addr, err := ds.ConnectWifiDirect(context.Background(), "x", "")
	if err != ErrWifiDirectUnsupported {
		t.Fatalf("ConnectWifiDirect = (%q, %v), want (_, %v)", addr, err, ErrWifiDirectUnsupported)
	}
}

func TestWifiDirectPeersDisabled(t *testing.T) {
	ds := NewDiscoveryService(nil, &SettingsService{cfg: Settings{}})
	peers, err := ds.WifiDirectPeers(context.Background())
	if peers != nil || err != nil {
		t.Fatalf("WifiDirectPeers = (%v, %v), want (nil, nil)", peers, err)
	}
}

func TestConnectWifiDirectInjectsDevice(t *testing.T) {
	const wantAddr = "192.168.49.1:9120"
	ds := NewDiscoveryService(nil, &SettingsService{cfg: Settings{}})
	ds.SetWifiDirectManager(&fakeWifiDirectManager{connectAddr: wantAddr})

	addr, err := ds.ConnectWifiDirect(context.Background(), "peer1", "Peer One")
	if err != nil {
		t.Fatalf("ConnectWifiDirect err = %v", err)
	}
	if addr != wantAddr {
		t.Fatalf("ConnectWifiDirect addr = %q, want %q", addr, wantAddr)
	}

	var found *Device
	for i := range ds.GetDevices() {
		if ds.GetDevices()[i].ID == "peer1" {
			found = &ds.GetDevices()[i]
		}
	}
	if found == nil {
		t.Fatalf("device peer1 not recorded in devices: %+v", ds.GetDevices())
	}
	if found.Address != wantAddr {
		t.Fatalf("device peer1 address = %q, want %q", found.Address, wantAddr)
	}
	if found.Name != "Peer One" {
		t.Fatalf("device peer1 name = %q, want %q", found.Name, "Peer One")
	}
}

// TestConnectWifiDirectFallsBackOnDefaultPort verifies the configured port
// overrides the platform default when settings carry a real port, and leaves
// the platform-provided address untouched when no port is configured (the
// zero-port case that direct Settings{} construction produces).
func TestConnectWifiDirectFallsBackOnDefaultPort(t *testing.T) {
	ds := NewDiscoveryService(nil, &SettingsService{cfg: Settings{Port: 9555}})
	ds.SetWifiDirectManager(&fakeWifiDirectManager{connectAddr: "192.168.49.10:9120"})

	addr, err := ds.ConnectWifiDirect(context.Background(), "peer2", "")
	if err != nil {
		t.Fatalf("ConnectWifiDirect err = %v", err)
	}
	if addr != "192.168.49.10:9555" {
		t.Fatalf("ConnectWifiDirect addr = %q, want %q", addr, "192.168.49.10:9555")
	}
}

// TestConnectWifiDirectGroupOwner verifies the group-owner path: when this
// device owns the group the peer's endpoint is unknown, and callers must see
// the dedicated sentinel so the UI can instruct the user accordingly.
func TestConnectWifiDirectGroupOwner(t *testing.T) {
	ds := NewDiscoveryService(nil, &SettingsService{cfg: Settings{}})
	ds.SetWifiDirectManager(&fakeWifiDirectManager{connectErr: ErrWifiDirectGroupOwner})

	addr, err := ds.ConnectWifiDirect(context.Background(), "peer3", "Owner Peer")
	if err != ErrWifiDirectGroupOwner {
		t.Fatalf("ConnectWifiDirect err = %v, want %v", err, ErrWifiDirectGroupOwner)
	}
	if addr != "" {
		t.Fatalf("ConnectWifiDirect addr = %q, want empty", addr)
	}
	// No device may be recorded for a connect that produced no endpoint.
	for _, dev := range ds.GetDevices() {
		if dev.ID == "peer3" {
			t.Fatalf("device peer3 recorded despite group-owner error: %+v", dev)
		}
	}
}
