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
3. **Transfer** — The sender streams files over plain HTTP/TCP to the receiver's device. Each file is hashed while it is being read, and the receiver returns its computed SHA-256 digest after the write completes.
4. **Accept/Reject** — The receiver sees an incoming file prompt and can accept or decline. Auto-accept can be enabled in settings.
5. **Progress** — Real-time progress, speed, and ETA are shown for every transfer. Pause, resume, and cancel are supported. Up to four files can transfer concurrently.

## Features

- **Zero Config** — Auto-discovery on LAN, no manual setup required
- **Cross-Platform** — Windows, macOS, Linux, Android, iOS via Wails v3
- **QR Pairing** — Scan a QR code or enter a 6-digit code to connect devices
- **Real-time Progress** — Live speed, ETA, and per-file progress tracking
- **Pause / Resume / Cancel** — Full transfer control at any time
- **High-throughput LAN Transfers** — One-pass file streaming, connection reuse, and up to four concurrent files
- **End-to-end Integrity** — Sender and receiver compare SHA-256 digests after each file completes
- **Accept / Reject** — Incoming files require consent (or auto-accept)
- **Transfer History** — Completed and failed transfers are logged
- **Dark Theme** — Industrial-utilitarian design with amber accents

## Tech Stack

| Layer | Technology |
|-------|------------|
| Backend | Go 1.25 + Wails v3 |
| Frontend | Vue 3 + TypeScript + Vite 8 + Tailwind CSS 3 |
| Discovery | UDP broadcast beacons (port 9129) |
| Transfer | Plain HTTP over TCP (port 9120, configurable) |
| History | JSON file (`~/.light/history.json`) |
| QR | [skip2/go-qrcode](https://github.com/skip2/go-qrcode) + jsQR (browser) |

## Transfer Performance and Compatibility

Light uses the existing local HTTP/TCP connection on all supported platforms. The current transfer engine is optimized without adding protocol dependencies or requiring platform-specific networking APIs:

- The sender reads each file once and computes SHA-256 while streaming it to the receiver.
- The receiver hashes and writes with a 1 MB streaming buffer, then performs one sync after the file completes.
- A shared HTTP transport reuses connections and supports up to four concurrent file uploads.
- One failed file is reported independently; other files in the same batch continue transferring.
- Pause, resume, and cancel remain scoped to each individual file.
- The receiver returns `ok <sha256>` after a successful write, and the sender verifies that digest before marking the transfer complete.

This is a LAN-only plain HTTP transport and does not currently use TLS, QUIC, Wi-Fi Direct, or mobile hotspot mode. Actual throughput depends on the Wi-Fi/Ethernet link, router congestion, device storage, and Android storage providers. For digest verification, both devices should run compatible current builds.

## Project Structure

```text
main.go                      Wails composition, embedded assets
internal/light/              Go backend package
  models.go                  Domain types (Device, Transfer, Settings, etc.)
  settings.go                SettingsService — config persistence + device ID
  discovery.go               DiscoveryService — UDP beacon broadcast + Diagnostics
  filetransfer.go            FileTransferService — HTTP server + sender upload
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

# Build Android APK
wails3 task android:package
```

## Configuration

Settings are stored in `~/.light/settings.json`:

```json
{
  "deviceName": "My Device",
  "port": 9120,
  "downloadDir": "~/Downloads/Light",
  "autoAccept": false,
  "theme": "dark",
  "enableEncryption": false
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `deviceName` | hostname | Name shown to other devices |
| `port` | 9120 | TCP port for the file transfer HTTP server |
| `downloadDir` | `~/Downloads/Light` | Where received files are saved |
| `autoAccept` | false | Accept incoming files without prompting |
| `theme` | dark | UI theme (dark only for now) |

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

## Verification

Run the following checks before submitting changes:

```bash
go test ./...
go test -race ./internal/light
go vet ./...
cd frontend && npm run build
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

<div align="center">

**Made with care for fast, private file sharing**

</div>
