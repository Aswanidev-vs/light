# Light - Current Features

This document describes behavior implemented in the current codebase. It is
kept separate from the roadmap so planned work is not presented as a shipped
feature.

## File sharing

- Bidirectional file transfers between Light peers.
- Plain HTTP over TCP is the stable default transport.
- Configurable transfer server port; the default is `9120`.
- Multiple files can be selected in one transfer request and uploaded with up
  to four concurrent workers.
- Every Light instance can send and receive. Incoming sender identity and
  address are remembered locally so the receiver can use the Share back action
  or select that peer from the Send view.
- Streaming progress events with transferred bytes, percentage, speed, and
  status.
- Active outgoing transfers support pause, resume, and cancel.
- Incoming transfers can be accepted or declined. Auto-accept is available in
  Settings.
- Destination names are sanitized and made collision-safe instead of
  overwriting an existing file.
- Sender-provided file size and SHA-256 checksum are sent with each upload.
  The receiver hashes the stream while writing and rejects a checksum
  mismatch.

Pause/resume applies to an active transfer. Interrupted transfers are not
resumed after an application restart or a broken network connection.

## Device discovery and pairing

- UDP beacon discovery on port `9129` for peers on the same LAN.
- Beacons include a persistent device ID, device name, desktop/mobile type,
  transfer port, pairing code, and timestamp.
- Devices expire from the live list after roughly 10 seconds without a beacon.
- Pairing by a six-digit code, with codes valid for five minutes.
- QR pairing tickets containing the peer address, device ID, name, port, and
  device type.
- QR scanning through the browser `BarcodeDetector` API with a `jsQR`
  fallback.
- Discovery diagnostics expose the local ID, listener state, interface count,
  sender count, peer count, and the last discovery error.

QR pairing can connect devices across subnets only when the advertised address
and transfer port are reachable. It does not create a network path by itself.

## Receiver storage and cleanup

- Desktop receivers write directly to the configured download folder.
- Android receivers stage files in app-private storage, then copy completed
  files into the folder selected through Android's Storage Access Framework.
- Android requires a selected SAF folder before receiving files.
- Failed or interrupted receive operations use a Light-owned partial filename
  and remove it when the operation ends unsuccessfully.
- Partial files older than 24 hours are pruned when the receive directory is
  used.
- Android picker copies and stale picker cache entries are cleaned after file
  selection and during app startup.

## History

- Completed, failed, and cancelled transfers are recorded in
  `~/.light/history.json` (app-private storage on Android).
- History is limited to the latest 500 entries.
- The History view can clear the stored transfer history.

## User interface

- Send, Receive, History, and Settings views.
- LAN device list with desktop/mobile icons and live status indicators.
- Drag-and-drop and native file picker support for sending files.
- Incoming transfer dialog with file names and sizes.
- Transfer cards with progress, speed, status, pause/resume, and cancel
  controls.
- Responsive desktop, tablet, and mobile layouts with a desktop sidebar,
  tablet navigation rail, mobile header, and mobile bottom navigation.
- Safe-area-aware mobile dialogs, notifications, and navigation for devices
  with status bars, cutouts, or gesture areas.
- Dark industrial-style interface with amber accents.

## Settings

- Device name.
- Download folder.
- Transfer port.
- Auto-accept incoming files.
- Experimental transport toggle.
- Settings and device identity persist in the app's `.light` configuration
  directory.

## Experimental QUIC transport

- Optional HTTP/3 over QUIC transport on the configured transfer port.
- TCP remains available as the fallback path.
- When QUIC mode is enabled for outgoing transfers, Light probes the peer
  first. A failed handshake, unavailable UDP path, or peer using TCP causes a
  fallback to TCP before the prepare request and file body are sent.
- QUIC uses an ephemeral self-signed certificate. Traffic is encrypted, but
  peer identity is not authenticated by a trust store yet.
- QUIC is experimental and is not guaranteed to be faster than TCP on every
  Wi-Fi, Ethernet, storage, or operating-system combination.

## Platform support

- Go 1.25 backend with Wails v3.
- Vue 3, TypeScript, Vite, and Tailwind CSS frontend.
- The project contains Wails desktop and mobile build configuration. The
  current GitHub workflow builds Windows and Android artifacts for releases.

## Not currently implemented

- Native Wi-Fi Direct connection management.
- Automatic hotspot creation or network switching.
- Resume-after-restart or chunk-level resume for interrupted files.
- PIN-based transfer authentication.
- SQLite persistence.
- Authenticated TLS identity for the experimental QUIC path.
