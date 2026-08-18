<div align="center">

<img src="logo.png" alt="Light" width="120" />

# Light

**Fast, local, private file sharing**

![Go](https://img.shields.io/badge/go-1.25-00ADD8?logo=go)
![Wails](https://img.shields.io/badge/wails-v3-FF5E1A)
![Vue](https://img.shields.io/badge/vue-3-42b883?logo=vue.js)
![Tailwind](https://img.shields.io/badge/tailwind-3-06b6d4?logo=tailwindcss)
![License](https://img.shields.io/badge/license-MIT-blue.svg)

</div>

---

## How It Works

1. **Discovery** — Devices broadcast their presence via UDP beacons on the local network (port 9129). Each device periodically sends a JSON heartbeat containing its ID, name, type, and transfer port. Other devices listen and maintain a live device table with a 10-second TTL.
2. **Pairing** — Scan a QR code or enter a 6-digit code to connect devices across subnets, or when auto-discovery doesn't reach them.
3. **Transfer** — The sender streams files over HTTP/3 (QUIC) by default, probing the peer first and falling back to TCP automatically when the peer does not support QUIC or UDP is unavailable. The first bytes start streaming as soon as the receiver accepts; the integrity hash runs in the background. Each file includes a SHA-256 checksum; the receiver hashes the incoming stream, rejects mismatches, and only then moves the partial file into place.
4. **Accept/Reject** — The receiver sees an incoming file prompt and can accept or decline. Auto-accept can be enabled in settings.
5. **Progress** — Real-time progress, speed, and ETA are shown for every transfer. Pause, resume, and cancel are supported.

Failed or interrupted receives are written to a Light-owned partial filename
and removed automatically. Stale partial artifacts older than 24 hours are
also pruned. On Android, temporary files created by the document picker are
removed after sending and stale picker cache is cleaned at app startup.

## Features

- **Zero Config** — Auto-discovery on LAN, no manual setup required
- **Cross-Platform** — Wails-based desktop and mobile targets, with Windows and Android builds in the current release workflow
- **QR Pairing** — Scan a QR code or enter a 6-digit code to connect devices
- **Real-time Progress** — Live speed, ETA, and per-file progress tracking
- **Pause / Resume / Cancel** — Full transfer control at any time
- **Accept / Reject** — Incoming files require consent (or auto-accept)
- **Bidirectional Sharing** — Each peer can send back to a remembered sender
- **Parallel Transfers** — Up to four selected files can upload concurrently
- **Transfer History** — Completed and failed transfers are logged
- **Safe file handling** — Collision-safe destination names plus automatic cleanup of failed and stale partial files
- **Dark Theme** — Industrial-utilitarian design with amber accents

See [docs/FEATURES.md](docs/FEATURES.md) for the complete current feature inventory and known limitations.

## HTTP/3 (QUIC) transport

QUIC is the default transport: outgoing transfers probe the peer's HTTP/3
`/api/capabilities` endpoint on the transfer port and fall back to TCP
before `/api/prepare` or any file body is sent when the peer does not
support QUIC or UDP is unavailable. Set the transport setting to `tcp` to
opt out; QUIC senders detect that and use plain TCP for such peers.

QUIC mode uses an ephemeral self-signed certificate: traffic is encrypted,
but peer identity is not authenticated yet.

To compare the local transport paths:

```bash
go test ./internal/light -run '^$' -bench '^BenchmarkTransferTransport$' -benchtime=1s
```

## Experimental Wi-Fi Direct (peer-to-peer)

Light can transfer files over a direct Wi-Fi Direct (P2P) link instead of an
access point, so two devices can share even when they are not on the same
Wi-Fi network. TCP/QUIC remains the stable default transport; Wi-Fi Direct only
changes *which network* the transfer uses, while `transportMode` still decides
*which protocol* (HTTP/TCP or QUIC) runs over it. The native adapters negotiate
the P2P link and return the peer's transfer address; the existing transfer stack
then rides on top unchanged.

The feature is opt-in: the `wifiDirect` setting (default off) enables the Send
view's Wi-Fi Direct panel, which scans for nearby peers, forms a group, and
transfers over the link.

**Bidirectional by design.** Once the P2P group is up, the normal LAN UDP
discovery beacons (port 9129) flow across the link, so each side appears in the
other's device list and can initiate a transfer either way. If this device ends
up as the *group owner*, its negotiated address is its own; the app surfaces
`ErrWifiDirectGroupOwner` and tells you to start the transfer from the other
device (the peer shows up in the regular list via the beacons).

**Supported platforms:**

- **Android** — fully implemented via the Java `WifiP2pManager` (JNI bridge in
  `wifidirect_android.go` / `wifidirect_android_bridge.go`). Requests
  `NEARBY_WIFI_DEVICES` on Android 13+ or location on older releases.
- **Windows** — implemented via the WinRT `WiFiDirectDevice` API
  (`wifidirect_windows.go`). Wi-Fi Direct needs the `proximity` device
  capability, which an unpackaged `.exe` does not get by default. The Inno
  Setup installer ships and registers a small **identity (sparse) package**
  (`build/windows/sparse/`) that grants `proximity` to `light.exe`. This
  registers automatically when **Windows Developer Mode** is enabled (free, no
  code-signing certificate needed) or when the package is signed with a trusted
  cert. If neither is in place, Wi-Fi Direct stays unavailable and the app
  silently falls back to the LAN path.
- **Linux** — implemented via `wpa_supplicant` (`wpa_cli`, `p2p_connect` with
  push-button auth) when the tooling is present.
- **macOS is NOT supported** — Apple only exposes MultipeerConnectivity, which
  cannot host the app's HTTP transfer server, so the toggle is hidden there.

**Status.** The platform backends, the JNI/WinRT bridge, and the Send-view flow
are implemented and the Go code builds, vets, and passes the Wi-Fi Direct unit
tests on Windows. End-to-end transfer on real hardware has not yet been
validated in CI; treat the feature as experimental until confirmed on a real
laptop + phone pair. On Windows, the `proximity` grant comes from the identity
package in `build/windows/sparse/`; without a signing certificate it registers
only under Developer Mode, so Windows Wi-Fi Direct is currently a
developer/enthusiast path — ordinary users should transfer over the LAN (same
Wi-Fi) instead.

## Tech Stack

| Layer | Technology |
|-------|------------|
| Backend | Go 1.25 + Wails v3 |
| Frontend | Vue 3 + TypeScript + Vite 8 + Tailwind CSS 3 |
| Discovery | UDP broadcast beacons (port 9129) |
| Transfer | Plain HTTP/TCP (port 9120, configurable) with optional HTTP/3 over QUIC |
| History | JSON file (`~/.light/history.json`) |
| QR | [skip2/go-qrcode](https://github.com/skip2/go-qrcode) + jsQR (browser) |

## Project Structure

```text
main.go                      Wails composition, embedded assets
internal/light/              Go backend package
  models.go                  Domain types (Device, Transfer, Settings, etc.)
  settings.go                SettingsService — config persistence + device ID
  discovery.go               DiscoveryService — UDP beacon broadcast + Diagnostics
  filetransfer.go            FileTransferService — HTTP server + sender upload
  quic.go                    Experimental HTTP/3 server/client + TCP fallback
  transport_bench_test.go    Local TCP-versus-QUIC integration test and benchmark
  transfermanager.go         TransferManager — progress tracking + JSON history
  qr.go                      QRCodeService — QR generation + pairing codes
  broadcast_windows.go       SO_BROADCAST (Windows, syscall)
  broadcast_unix.go          SO_BROADCAST (Linux/macOS/Android, syscall)
frontend/                    Vue 3 application + generated Wails bindings
  src/
    composables/             Reactive stores (useDiscovery, useTransfers, useSettings, useUI)
    components/              UI components (devices, transfer, pair, common, layout)
    views/                   Page views (Send, Receive, History, Settings)
    styles/                  Tailwind CSS + design tokens
    lib/                     Helpers (event listener, byte formatting)
build/                       Wails platform/build configuration
```

## Download

| Platform | Link |
|----------|------|
| Windows | [Installer](https://github.com/Aswanidev-vs/light/releases/latest) |
| Android | [APK](https://github.com/Aswanidev-vs/light/releases/latest) |

## Quick Start

### Prerequisites

- [Go 1.25+](https://go.dev/dl/)
- [Node.js 22+](https://nodejs.org/)
- [Wails3 CLI](https://v3.wails.io/) (`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.2`)
- Android builds additionally require JDK 17+, Android SDK Platform 35, and
  Android NDK `26.3.11579264`.

### Development

```bash
# Clone the repository
git clone https://github.com/Aswanidev-vs/light.git
cd light

# Install dependencies
cd frontend && npm install && cd ..

# Start development server
wails3 dev
```

### Build

```bash
# Build for current platform
wails3 build

# Build Windows
GOOS=windows wails3 build

# Build an Android APK (requires the Android SDK and NDK)
wails3 task android:package

# Build an APK containing arm64-v8a and x86_64 binaries
wails3 task android:package:fat
```

The Android build uses `minSdk 23`, `targetSdk 35`, and the NDK version shown
above. Set `ANDROID_HOME` or `ANDROID_SDK_ROOT` when the SDK is not installed
in the default location; set `ANDROID_NDK_HOME` to select a specific NDK.

## Configuration

Settings are stored in `~/.light/settings.json`:

```json
{
  "deviceName": "My Device",
  "port": 9120,
  "downloadDir": "~/Downloads/Light",
  "autoAccept": false,
  "theme": "dark",
  "transportMode": "tcp"
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `deviceName` | hostname | Name shown to other devices |
| `port` | 9120 | TCP port for the file transfer HTTP server |
| `downloadDir` | `~/Downloads/Light` | Where received files are saved |
| `autoAccept` | false | Accept incoming files without prompting |
| `theme` | dark | UI theme (dark only for now) |
| `transportMode` | tcp | `tcp` for the stable path, or `quic` to try HTTP/3 first and fall back to TCP |
| `wifiDirect` | false | Enables the experimental Wi-Fi Direct (peer-to-peer) backend and the Send-view peer scan/connect panel |

## Architecture

```
┌─────────────────────────────────────────────────┐
│                   Frontend (Vue 3)              │
│  SendView · ReceiveView · HistoryView · Settings │
│         ↕ Events.On / wails.Call                │
├─────────────────────────────────────────────────┤
│               Wails v3 bindings                  │
│         ↕ app.Event.Emit / exported methods      │
├─────────────────────────────────────────────────┤
│                 Go Backend                       │
│  DiscoveryService ── UDP broadcast (9129)        │
│  FileTransferService ── HTTP server (9120)       │
│  TransferManager ── progress + JSON history      │
│  QRCodeService ── QR generation + pairing codes  │
│  SettingsService ── config persistence           │
└─────────────────────────────────────────────────┘
         ↕ UDP beacons         ↕ HTTP transfer
┌──────────────┐         ┌──────────────┐
│  Peer Device  │ ◄─────► │  Peer Device  │
└──────────────┘         └──────────────┘
```

## Discovery Protocol

The old mDNS-based discovery was replaced with a simpler, more reliable **UDP beacon broadcast** protocol:

- **Port**: 9129 (fixed UDP)
- **Beacon payload**: `{ id, name, type, port, pairingCode, ts }`
- **Send**: Per-interface directed broadcast + `255.255.255.255` + loopback (all unconditionally, not fallback-on-error)
- **Receive**: Wildcard UDP socket on `0.0.0.0:9129`
- **Device ID**: Persisted `crypto/rand` UUID in `~/.light/deviceid` (never derived from a cert, never a constant)
- **Diagnostics**: `Diagnostics()` RPC + loopback self-test separates "socket broken" vs "packets not crossing network" vs "peer not answering"

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

<div align="center">

**Made with care for fast, private file sharing**

</div>
