//go:build linux && !android

package light

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"
)

// transferPort is the fixed TCP port the app's LAN transfer server listens on.
const transferPort = "9120"

type wpaManager struct {
	groupIface string
}

func newPlatformWifiDirectManager() (WifiDirectManager, error) {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		// wpa_supplicant control tooling is required for P2P; absence means
		// this platform cannot run Wi-Fi Direct.
		return nil, ErrWifiDirectUnsupported
	}
	return &wpaManager{}, nil
}

func (m *wpaManager) Discover(ctx context.Context) ([]WifiDirectPeer, error) {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		return nil, ErrWifiDirectUnsupported
	}

	findCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Start a P2P device discovery scan.
	_, _ = run(findCtx, "wpa_cli", "p2p_find")

	// Give nearby devices a moment to respond before listing them.
	waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
	defer waitCancel()
	_ = sleepCtx(waitCtx, 3*time.Second)

	out, err := run(ctx, "wpa_cli", "p2p_peers")
	if err != nil {
		return nil, ErrWifiDirectUnsupported
	}

	var peers []WifiDirectPeer
	for _, line := range strings.Split(out, "\n") {
		mac := strings.TrimSpace(line)
		if mac == "" {
			continue
		}
		peers = append(peers, WifiDirectPeer{ID: mac, Name: mac})
	}
	return peers, nil
}

func (m *wpaManager) Connect(ctx context.Context, peerID string) (string, error) {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		return "", ErrWifiDirectUnsupported
	}

	// Form a P2P group with the peer using push-button (PBC) auth.
	if _, err := run(ctx, "wpa_cli", "p2p_connect", peerID, "pbc"); err != nil {
		return "", ErrWifiDirectUnsupported
	}

	iface, err := m.findGroupInterface(ctx)
	if err != nil {
		return "", err
	}
	m.groupIface = iface

	ip, err := interfaceIPv4(ctx, iface)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, transferPort), nil
}

// findGroupInterface discovers the P2P group virtual interface (named p2p-*).
func (m *wpaManager) findGroupInterface(ctx context.Context) (string, error) {
	out, err := run(ctx, "wpa_cli", "p2p_group_interface", "list")
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			iface := strings.TrimSpace(line)
			if strings.HasPrefix(iface, "p2p-") {
				return iface, nil
			}
		}
	}
	// Fallback: scan known interfaces for a p2p- prefixed name.
	out, err = run(ctx, "ip", "-o", "link", "show")
	if err != nil {
		return "", ErrWifiDirectUnsupported
	}
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, "p2p-")
		if idx < 0 {
			continue
		}
		// Interface name sits before the trailing ":" that follows it.
		rest := line[idx:]
		if end := strings.Index(rest, ":"); end > 0 {
			return rest[:end], nil
		}
	}
	return "", ErrWifiDirectUnsupported
}

// interfaceIPv4 parses the first IPv4 address from "ip -4 addr show <iface>".
func interfaceIPv4(ctx context.Context, iface string) (string, error) {
	out, err := run(ctx, "ip", "-4", "addr", "show", iface)
	if err != nil {
		return "", ErrWifiDirectUnsupported
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "inet ") {
			continue
		}
		fields := strings.Fields(line)
		// fields: ["inet", "1.2.3.4/24", "..."]
		if len(fields) >= 2 {
			ip := strings.SplitN(fields[1], "/", 2)[0]
			if net.ParseIP(ip) != nil {
				return ip, nil
			}
		}
	}
	return "", ErrWifiDirectUnsupported
}

func (m *wpaManager) Close() error {
	// Best-effort teardown; ignore all errors.
	_, _ = run(context.Background(), "wpa_cli", "p2p_flush")
	if m.groupIface != "" {
		_, _ = run(context.Background(), "wpa_cli", "p2p_group_remove", m.groupIface)
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
