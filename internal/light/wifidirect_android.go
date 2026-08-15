//go:build android

package light

// Android Wi-Fi Direct backend.
//
// On Android, Go is compiled into a c-shared library (libwails) loaded by the
// Java host (build/android/app/src/main/java/com/wails/app/WailsBridge.java).
// The host owns the Android Wi-Fi Direct APIs (WifiP2pManager) because those are
// Java-only, and bridges discovered peers + the negotiated group IP back to Go
// through JNI. Wails v3 already establishes this native<->Java bridge (see the
// application_android.go cgo JNI helpers in the wails module); we follow the same
// convention: Go exports `Java_com_wails_app_WailsBridge_nativeWifiDirect*`
// functions that the Java host calls, and Go calls back into Java public methods
// on the bridge (wifiDirectStartDiscovery / wifiDirectConnect /
// wifiDirectCloseGroup) which the companion file
// wifidirect_android_bridge.go (cgo) dispatches via JNI.
//
// The Go side only stores state and orchestrates the synchronous handshake:
//   - Discover(): ask the host to start discovery, then return the peer list
//     that the host streams back via nativeWifiDirectReportPeers.
//   - Connect(peerID): ask the host to connect to the peer (peerID == the
//     WifiP2pDevice.deviceAddress the host reported), then block until the host
//     delivers the group owner address via nativeWifiDirectConnected.
//   - Close(): ask the host to tear the group down (removeGroup).
//
// Required AndroidManifest.xml permissions (add if missing):
//   <uses-permission android:name="android.permission.NEARBY_WIFI_DEVICES"
//       android:usesPermissionFlags="neverForLocation" tools:targetApi="33" />
//   <uses-permission android:name="android.permission.CHANGE_WIFI_STATE" />
//   <uses-permission android:name="android.permission.ACCESS_WIFI_STATE" />
//   (ACCESS_FINE_LOCATION / ACCESS_COARSE_LOCATION are already declared and are
//    required on API < 33 for Wi-Fi Direct discovery/connect.)

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// wifiDirectTransferPort is the app's transfer-service port the existing
// HTTP/TCP (and optional QUIC) stack listens on; the peer connects to it over
// the freshly formed P2P link.
const wifiDirectTransferPort = 9120

// androidWifiDirectManager holds the latest discovery/connection state pushed
// from the Java host and signals waiting Discover/Connect calls.
type androidWifiDirectManager struct {
	mu       sync.Mutex
	peers    []WifiDirectPeer
	peerByID map[string]WifiDirectPeer

	// connIP is the group address delivered by the host once the link is up;
	// connOwner records whether THIS device became the group owner (in which
	// case connIP is our own address and the peer's is unknown until it dials
	// us); connErr flags a failed handshake. All three are read by Connect.
	connIP    string
	connOwner bool
	connErr   error

	// lastErr records the most recent error reported via nativeWifiDirectError
	// so Discover can surface "manager unavailable" instead of returning an
	// empty list silently.
	lastErr string

	// discoverCh / connectCh are re-created per operation (buffered, cap 1) so a
	// Java callback can wake exactly the goroutine that is waiting.
	discoverCh chan struct{}
	connectCh  chan struct{}
}

// wdManager is the live manager instance. The JNI export handlers (Java -> Go)
// in wifidirect_android_bridge.go update it, so we keep a package-level handle.
var wdManager *androidWifiDirectManager

func newPlatformWifiDirectManager() (WifiDirectManager, error) {
	m := &androidWifiDirectManager{
		peerByID: map[string]WifiDirectPeer{},
	}
	wdManager = m
	return m, nil
}

func (m *androidWifiDirectManager) Discover(ctx context.Context) ([]WifiDirectPeer, error) {
	// If the Java bridge is not wired up (e.g. libwails not initialised) there is
	// nothing we can do on this platform.
	if !wdBridgeReady() {
		return nil, ErrWifiDirectUnsupported
	}

	m.mu.Lock()
	ch := make(chan struct{}, 1)
	m.discoverCh = ch
	m.lastErr = ""
	m.mu.Unlock()

	// Trigger WifiP2pManager.discoverPeers() on the host. Peer updates arrive
	// asynchronously via nativeWifiDirectReportPeers, which signals ch.
	wdStartDiscovery()

	select {
	case <-ch:
	case <-ctx.Done():
		// Return whatever we have so far; the caller may Discover again.
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]WifiDirectPeer, len(m.peers))
	copy(out, m.peers)
	if len(out) == 0 && m.lastErr != "" {
		return nil, fmt.Errorf("wifi-direct: %s", m.lastErr)
	}
	return out, nil
}

func (m *androidWifiDirectManager) Connect(ctx context.Context, peerID string) (string, error) {
	if !wdBridgeReady() {
		return "", ErrWifiDirectUnsupported
	}

	m.mu.Lock()
	ch := make(chan struct{}, 1)
	m.connectCh = ch
	m.connIP = ""
	m.connOwner = false
	m.connErr = nil
	m.lastErr = ""
	m.mu.Unlock()

	// Ask the host to connect to the peer. peerID is the WifiP2pDevice
	// deviceAddress reported earlier. The resulting group owner address arrives
	// via nativeWifiDirectConnected (or nativeWifiDirectError on failure).
	wdConnect(peerID)

	select {
	case <-ch:
		m.mu.Lock()
		ip := m.connIP
		owner := m.connOwner
		err := m.connErr
		m.mu.Unlock()
		if err != nil || ip == "" {
			return "", ErrWifiDirectUnsupported
		}
		// When we became the group owner, ip is our own group address; the
		// peer's transfer endpoint is only known once it dials us. Normal LAN
		// beacons still flow across the fresh link, so the peer shows up in
		// the regular device list.
		if owner {
			return "", ErrWifiDirectGroupOwner
		}
		return fmt.Sprintf("%s:%d", ip, wifiDirectTransferPort), nil
	case <-ctx.Done():
		return "", ErrWifiDirectUnsupported
	}
}

func (m *androidWifiDirectManager) Close() error {
	// Tell the host to removeGroup(); best-effort, ignore errors.
	wdCloseGroup()

	m.mu.Lock()
	m.peers = nil
	m.peerByID = map[string]WifiDirectPeer{}
	m.connIP = ""
	m.connErr = nil
	m.mu.Unlock()
	return nil
}

// --- Java -> Go notifications (invoked by the cgo JNI export handlers) -------

// wdNotifyPeers stores the latest peer list (JSON array of {id,name}) pushed by
// the host and wakes any awaiting Discover call.
func wdNotifyPeers(jsonStr string) {
	m := wdManager
	if m == nil {
		return
	}
	var raw []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return
	}

	m.mu.Lock()
	m.peers = m.peers[:0]
	for _, p := range raw {
		if p.ID == "" {
			continue
		}
		peer := WifiDirectPeer{ID: p.ID, Name: p.Name}
		m.peers = append(m.peers, peer)
		m.peerByID[p.ID] = peer
	}
	ch := m.discoverCh
	m.mu.Unlock()

	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wdNotifyConnected records the negotiated group address and wakes Connect.
// jsonStr is {"ip":"...","owner":true|false}; owner is true when THIS device
// became the group owner, in which case the IP is our own group address.
func wdNotifyConnected(jsonStr string) {
	m := wdManager
	if m == nil {
		return
	}
	var info struct {
		IP    string `json:"ip"`
		Owner bool   `json:"owner"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &info); err != nil {
		return
	}

	m.mu.Lock()
	m.connIP = info.IP
	m.connOwner = info.Owner
	ch := m.connectCh
	m.mu.Unlock()

	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wdNotifyError flags a failed handshake so a waiting Connect returns
// ErrWifiDirectUnsupported instead of blocking until ctx expiry. It also wakes
// a waiting Discover so the failure (e.g. missing permission) surfaces through
// Discover's lastErr path instead of timing out silently.
func wdNotifyError(msg string) {
	m := wdManager
	if m == nil {
		return
	}
	m.mu.Lock()
	m.connErr = ErrWifiDirectUnsupported
	m.lastErr = msg
	connectCh := m.connectCh
	discoverCh := m.discoverCh
	m.mu.Unlock()

	for _, ch := range []chan struct{}{connectCh, discoverCh} {
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}
