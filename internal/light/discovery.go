package light

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	announceInterval = 3 * time.Second
	deviceTTL        = 10 * time.Second
	pairingCodeTTL   = 5 * time.Minute
)

// beacon is the UDP presence advertisement sent on the LAN.
type beacon struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Port       int    `json:"port"`
	PairingCode string `json:"pairingCode"`
	TS         int64  `json:"ts"`
}

// DiscoveryService finds peer Light devices on the local network via UDP beacons.
//
// Design (deliberately simple + debuggable, unlike the old mDNS approach that
// "found nothing"):
//   - ONE wildcard UDP receiver bound to 0.0.0.0:<discoveryPort> captures beacons
//     from every interface.
//   - N per-interface directed-broadcast senders (plus 255.255.255.255 and the
//     loopback address) ALWAYS broadcast our beacon. These are not fallbacks that
//     trigger only on error — they all run unconditionally.
//   - The device ID is a persisted crypto/rand UUID, never derived from a cert,
//     so beacons never accidentally self-filter.
type DiscoveryService struct {
	app      *application.App
	settings *SettingsService

	mu       sync.RWMutex
	devices  map[string]*Device
	code     string
	codeExp  time.Time
	selfType DeviceType

	conn     *net.UDPConn
	senders  []*net.UDPConn
	closed   bool
	lastErr  string

	stopOnce sync.Once
	stopChan chan struct{}
}

func NewDiscoveryService(app *application.App, settings *SettingsService) *DiscoveryService {
	return &DiscoveryService{
		app:      app,
		settings: settings,
		devices:  make(map[string]*Device),
		selfType: PlatformDeviceType(),
		stopChan: make(chan struct{}),
	}
}

func (d *DiscoveryService) SetApp(app *application.App) { d.app = app }

func (d *DiscoveryService) Start() error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: discoveryPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		d.mu.Lock()
		d.lastErr = fmt.Sprintf("listen: %v", err)
		d.mu.Unlock()
		return err
	}
	d.mu.Lock()
	d.conn = conn
	d.closed = false
	d.mu.Unlock()

	go d.recvLoop(conn)
	go d.announceLoop()
	go d.expireLoop()
	return nil
}

func (d *DiscoveryService) Stop() {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		close(d.stopChan)
		if d.conn != nil {
			d.conn.Close()
		}
		for _, s := range d.senders {
			s.Close()
		}
		d.senders = nil
		d.mu.Unlock()
	})
}

func (d *DiscoveryService) recvLoop(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			d.mu.RLock()
			closed := d.closed
			d.mu.RUnlock()
			if closed {
				return
			}
			d.mu.Lock()
			d.lastErr = fmt.Sprintf("recv: %v", err)
			d.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var b beacon
		if err := json.Unmarshal(buf[:n], &b); err != nil {
			continue
		}
		d.handleBeacon(b, raddr)
	}
}

func (d *DiscoveryService) handleBeacon(b beacon, raddr *net.UDPAddr) {
	if b.ID == "" {
		return
	}
	// APIPA/link-local addresses usually belong to a disconnected or
	// auto-configured adapter and are not valid LAN endpoints for transfers.
	if raddr == nil || raddr.IP.IsLinkLocalUnicast() {
		return
	}
	// Self-filter using the stable device ID (never a constant).
	if b.ID == d.settings.DeviceID() {
		return
	}

	port := b.Port
	if port == 0 {
		port = d.settings.GetSettings().Port
	}
	address := fmt.Sprintf("%s:%d", raddr.IP.String(), port)

	d.mu.Lock()
	existing, ok := d.devices[b.ID]
	isNew := !ok
	if !ok {
		existing = &Device{ID: b.ID}
		d.devices[b.ID] = existing
	}
	existing.Name = b.Name
	existing.Type = DeviceType(b.Type)
	existing.Address = address
	existing.Code = b.PairingCode
	existing.LastSeen = time.Now()
	d.mu.Unlock()

	if isNew && d.app != nil {
		d.app.Event.Emit("device-found", *existing)
	}
}

func (d *DiscoveryService) expireLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopChan:
			return
		case <-ticker.C:
			now := time.Now()
			var expired []string
			d.mu.Lock()
			for id, dev := range d.devices {
				if now.Sub(dev.LastSeen) > deviceTTL {
					expired = append(expired, id)
					delete(d.devices, id)
				}
			}
			d.mu.Unlock()
			for _, id := range expired {
				if d.app != nil {
					d.app.Event.Emit("device-lost", map[string]string{"id": id})
				}
			}
		}
	}
}

func (d *DiscoveryService) announceLoop() {
	ticker := time.NewTicker(announceInterval)
	defer ticker.Stop()
	d.announce()
	for {
		select {
		case <-d.stopChan:
			return
		case <-ticker.C:
			d.announce()
		}
	}
}

func (d *DiscoveryService) announce() {
	d.mu.RLock()
	closed := d.closed
	d.mu.RUnlock()
	if closed {
		return
	}

	code := d.currentPairingCode()
	cfg := d.settings.GetSettings()
	b := beacon{
		ID:         d.settings.DeviceID(),
		Name:       cfg.DeviceName,
		Type:       string(d.selfType),
		Port:       cfg.Port,
		PairingCode: code,
		TS:         time.Now().Unix(),
	}
	data, err := json.Marshal(b)
	if err != nil {
		return
	}

	for _, s := range d.ensureSenders() {
		if _, err := s.Write(data); err != nil {
			d.mu.Lock()
			d.lastErr = fmt.Sprintf("send: %v", err)
			d.mu.Unlock()
		}
	}
}

// ensureSenders builds the broadcast socket set once: global broadcast, loopback,
// and a directed-broadcast socket per non-loopback IPv4 interface.
func (d *DiscoveryService) ensureSenders() []*net.UDPConn {
	d.mu.Lock()
	if d.senders != nil {
		s := d.senders
		d.mu.Unlock()
		return s
	}
	d.mu.Unlock()

	var conns []*net.UDPConn
	add := func(ip net.IP) {
		c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: discoveryPort})
		if err != nil {
			return
		}
		_ = setBroadcast(c)
		conns = append(conns, c)
	}
	add(net.IPv4bcast) // 255.255.255.255
	add(net.IPv4(127, 0, 0, 1))

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				if !isUsableLANIPv4(ipnet.IP) {
					continue
				}
				if bcast := directedBroadcast(ipnet); bcast != nil {
					add(bcast)
				}
			}
		}
	}

	d.mu.Lock()
	d.senders = conns
	d.mu.Unlock()
	return conns
}

func directedBroadcast(ipnet *net.IPNet) net.IP {
	ip := ipnet.IP.To4()
	if ip == nil || len(ipnet.Mask) != 4 {
		return nil
	}
	out := make(net.IP, 4)
	for i := range out {
		out[i] = ip[i] | ^ipnet.Mask[i]
	}
	return out
}

func (d *DiscoveryService) currentPairingCode() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.code != "" && time.Now().Before(d.codeExp) {
		return d.code
	}
	d.code = fmt.Sprintf("%06d", rand6())
	d.codeExp = time.Now().Add(pairingCodeTTL)
	return d.code
}

func rand6() int {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return int(uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3])) % 1000000
}

func (d *DiscoveryService) GetDevices() []Device {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Device, 0, len(d.devices))
	for _, dev := range d.devices {
		out = append(out, *dev)
	}
	return out
}

func (d *DiscoveryService) PairByCode(code string) *Device {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, dev := range d.devices {
		if dev.Code == code {
			c := *dev
			return &c
		}
	}
	return nil
}

func (d *DiscoveryService) GetDevicePairingCode() string { return d.currentPairingCode() }

// LocalEndpoint returns this device's transfer address as host:port (used for QR).
func (d *DiscoveryService) LocalEndpoint() string {
	ip, _ := firstLANIPv4()
	return fmt.Sprintf("%s:%d", ip, d.settings.GetSettings().Port)
}

type Diagnostics struct {
	MyID      string `json:"myId"`
	Listening bool   `json:"listening"`
	Devices   int    `json:"devices"`
	Interfaces int   `json:"interfaces"`
	Senders   int    `json:"senders"`
	LastError string `json:"lastError"`
}

func (d *DiscoveryService) Diagnostics() Diagnostics {
	d.mu.RLock()
	diag := Diagnostics{
		MyID:      d.settings.DeviceID(),
		Listening: d.conn != nil,
		Devices:   len(d.devices),
		Senders:   len(d.senders),
		LastError: d.lastErr,
	}
	d.mu.RUnlock()
	if ifaces, err := net.Interfaces(); err == nil {
		diag.Interfaces = len(ifaces)
	}
	return diag
}

func firstLANIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if v4 := ipnet.IP.To4(); isUsableLANIPv4(v4) {
					return v4.String(), nil
				}
			}
		}
	}
	return "127.0.0.1", fmt.Errorf("no LAN IPv4 address found")
}

func isUsableLANIPv4(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && !v4.IsLoopback() && !v4.IsLinkLocalUnicast() && !v4.IsUnspecified()
}
