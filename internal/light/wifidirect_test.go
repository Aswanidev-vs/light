package light

import (
	"context"
	"testing"
)

// fakeWifiDirectManager is an in-memory WifiDirectManager for tests; it never
// touches hardware.
type fakeWifiDirectManager struct {
	connectAddr string
}

func (f *fakeWifiDirectManager) Discover(ctx context.Context) ([]WifiDirectPeer, error) {
	return nil, nil
}

func (f *fakeWifiDirectManager) Connect(ctx context.Context, peerID string) (string, error) {
	return f.connectAddr, nil
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
	addr, err := ds.ConnectWifiDirect(context.Background(), "x")
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

	addr, err := ds.ConnectWifiDirect(context.Background(), "peer1")
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
}
