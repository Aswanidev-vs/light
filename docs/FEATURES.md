# Light - Current Features

This document describes behavior implemented in the current codebase. It is
kept separate from the roadmap so planned work is not presented as a shipped
feature. Where a platform capability exists in the native/runtime layer but is
not currently used by the Light UI, it is labelled as an API/runtime feature
instead of a user-facing feature.

## File sharing

- Bidirectional file transfers between Light peers.
- Plain HTTP over TCP is the stable default transport.
- Configurable transfer server port; the default is `9120`.
- Multiple files can be selected in one transfer request and uploaded with up
  to four concurrent workers on desktop and two on mobile.
- Every Light instance can send and receive. Incoming sender identity and
  address are remembered locally so the receiver can use the Share back action
  or select that peer from the Send view.
- A batch continues processing other files when an individual file fails, and
  reports the failed files together at the end.
- Streaming progress events with transferred bytes, percentage, speed, and
  status.
- Active outgoing transfers support pause, resume, and cancel.
- Incoming transfers can be accepted or declined. Auto-accept is available in
  Settings.
- A receiver acceptance request waits for up to two minutes before the sender
  marks the batch rejected or timed out.
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
- Explicitly paired peers are remembered in browser local storage and remain
  available for reverse sharing after their discovery beacon expires.
- Refreshing discovery rebuilds broadcast senders so changed Wi-Fi or Ethernet
  interfaces are picked up without restarting the app.
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
- Android selected files are copied into an app cache before Go receives paths;
  only that owned picker cache is removed during cleanup.

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
- Incoming requests support selecting individual files before accepting.
- Transfer cards with progress, speed, status, pause/resume, and cancel
  controls.
- Pull-to-refresh and button refresh for the nearby-device list, toast feedback,
  and a Share back action for the last incoming peer.
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
- Experimental QUIC/HTTP-3 transport toggle.
- Experimental Wi-Fi Direct toggle when the platform reports support.
- Android SAF folder URI, stored separately from its human-readable folder
  label.
- Settings and device identity persist in the app's `.light` configuration
  directory.

The persisted settings model also contains `enableEncryption` for compatibility,
but the current transfer path does not use it and the current Settings view does
not expose it.

## Experimental QUIC transport

- Optional HTTP/3 over QUIC transport on the configured transfer port.
- TCP remains available as the fallback path.
- When QUIC mode is enabled for outgoing transfers, Light probes the peer
  first. A failed handshake, unavailable UDP path, or peer using TCP causes a
  fallback to TCP before the prepare request and file body are sent.
- QUIC uses an ephemeral self-signed certificate. Traffic is encrypted, but
  peer identity is not authenticated by a trust store yet.
- The QUIC capability probe is cached per peer for 60 seconds, and the server
  keeps TCP available if UDP/QUIC setup fails.
- QUIC is experimental and is not guaranteed to be faster than TCP on every
  Wi-Fi, Ethernet, storage, or operating-system combination.

## Wi-Fi Direct implementation status

- Platform managers are implemented for Android (`WifiP2pManager`), Windows
  (WinRT `WiFiDirectDevice`), and Linux (`wpa_cli`/`wpa_supplicant`). macOS
  returns unsupported because it does not expose a compatible raw Wi-Fi Direct
  API.
- The managers discover peers, form a P2P group, return the negotiated transfer
  address, and tear the group down. The existing HTTP/TCP or optional QUIC
  transfer stack is reused after a link is established.
- Android uses a JNI bridge between Go and the Java host, requests
  `NEARBY_WIFI_DEVICES` on Android 13+ or location permission on older releases,
  and listens for peer and connection-info broadcasts. P2P system receivers are
  registered with `RECEIVER_EXPORTED` on API 26+, and discovery/connect calls
  are dispatched to the UI thread before touching `WifiP2pManager`.
- When this device ends up as the group owner, the negotiated IP is its own;
  the connection-info callback reports an `owner` flag so Go returns the
  group-owner error instead of trying to transfer to itself. The frontend then
  tells the user to start the transfer from the other device (regular LAN
  beacons still flow across the fresh link).
- The `wifiDirect` setting toggles a full end-to-end flow: the Send view shows a
  Wi-Fi Direct panel that scans via `WifiDirectPeers`, connects via
  `ConnectWifiDirect(peerID, peerName)`, injects the peer into the device list
  (persisted past beacon expiry), and tears the group down on disconnect.

## Android native/runtime implementation

The Android host provides the following implemented runtime capabilities to the
Wails application. These are available through the native bridge; not all are
currently used by Light's feature screens.

- WebView asset serving, JavaScript/runtime message dispatch, Go lifecycle
  callbacks, main-thread dispatch, and low-memory/page-finished notifications.
- Native file and folder pickers, multi-file selection, persistable Storage
  Access Framework permissions, staged downloads, URI-backed destination copies,
  and safe picker-cache cleanup.
- Screen/device/app information, dark-mode detection, safe-area insets,
  orientation control, status-bar control, brightness control, clipboard,
  share chooser, external URL opening, toast messages, keep-awake, torch, and
  vibration/haptic feedback.
- Biometric/device-credential authentication and encrypted key-value storage
  backed by AndroidX `EncryptedSharedPreferences`.
- Local notifications with Android 13 notification-permission handling and a
  sticky data-sync foreground service for work that must keep the process alive
  in the background.
- Camera photo/video capture with cache-backed thumbnails and range-enabled
  WebView streaming, plus location, motion, proximity, text-to-speech, storage,
  power, and network information/event bridges.
- Wi-Fi multicast locking so UDP LAN discovery can receive packets while the
  Android app is running.

## Platform support

- Go 1.25 backend with Wails v3.
- Vue 3, TypeScript, Vite, and Tailwind CSS frontend.
- Android uses a WebView host with `minSdk 23`, `targetSdk 35`, and NDK
  `26.3.11579264`.
- The current GitHub workflow builds Windows and Android artifacts for releases;
  Android produces arm64-v8a and x86_64 binaries.

## Not currently implemented

- Automatic hotspot creation or network switching.
- Resume-after-restart or chunk-level resume for interrupted files.
- PIN-based transfer authentication.
- SQLite persistence.
- Authenticated TLS identity for the experimental QUIC path.
