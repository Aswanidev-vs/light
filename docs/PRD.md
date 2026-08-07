# Light — Product Requirements Document

## 1. Product Vision

Light is a high-speed, cross-platform file sharing application built with Wails3 (Go backend + Vue 3 frontend). It enables seamless file transfers between devices on the same local network with a focus on speed, simplicity, and reliability.

**Tagline:** Fast, local, private file sharing.

## 2. Target Users

- Individuals who need to quickly share files between their own devices (phone to laptop, laptop to desktop)
- Small teams working in the same office/room who need fast file transfer without cloud dependency
- Users who prioritize privacy — all transfers happen on the local network, no cloud servers involved

## 3. Platform Support

| Platform | Status | Build Method |
|----------|--------|--------------|
| Windows (x64) | Supported | Wails3 + NSIS installer |
| macOS (arm64/x64) | Supported | Wails3 + .app bundle |
| Linux (x64/arm64) | Supported | Wails3 + AppImage/deb |
| Android (arm64/arm) | Supported | Wails3 + Gradle APK |
| iOS (arm64) | Planned | Wails3 + Xcode |

## 4. Core Features

### 4.1 Device Discovery
- Automatic discovery of other Light instances on the local network
- Uses mDNS/UDP broadcast for zero-configuration discovery
- Displays device name, type (desktop/mobile), and connection status
- Real-time device list updates (devices appear/disappear)

### 4.2 File Sending
- Drag-and-drop file selection
- Multi-file selection via file picker
- Folder/directory sending (recursive)
- File size display and validation
- Send to any discovered device

### 4.3 File Receiving
- Incoming transfer notification with accept/reject prompt
- Auto-accept mode (configurable per device or globally)
- Download to default or custom directory
- File type preview (image thumbnails, text previews)

### 4.4 Transfer Management
- Real-time progress bars with speed indicators
- Transfer queue with pause/resume/cancel
- Concurrent transfer support (up to 4 simultaneous)
- Transfer history with status (completed/failed/cancelled)
- Resume interrupted transfers

### 4.5 Settings
- Default download directory
- Auto-accept incoming transfers (toggle)
- Network port configuration
- Device name customization
- Theme preference (dark/light)

## 5. Non-Functional Requirements

### 5.1 Performance
- Transfer speed: saturate local network bandwidth (100Mbps+ on WiFi, 1Gbps+ on Ethernet)
- Chunk size: 64KB default, configurable
- Memory usage: under 100MB idle, under 200MB during active transfers
- Startup time: under 2 seconds on desktop, under 3 seconds on mobile

### 5.2 Security
- All transfers are local network only — no internet/cloud dependency
- Optional transfer encryption (AES-256-GCM) for sensitive files
- Device verification via handshake protocol

### 5.3 Reliability
- Transfer resume support for interrupted connections
- Checksum verification (SHA-256) after transfer completion
- Graceful handling of network interruptions

### 5.4 Usability
- Zero configuration for basic usage (just install and share)
- Intuitive drag-and-drop interface
- Clear visual feedback for all operations
- Responsive design for both desktop and mobile viewports

## 6. Technical Architecture

```
┌─────────────────────────────────────────────────┐
│                  Frontend (Vue 3)                │
│  ┌──────────┐ ┌──────────┐ ┌──────────────────┐ │
│  │ Sidebar  │ │ Transfer │ │    Settings      │ │
│  │  (Devices│ │   Area   │ │    Panel         │ │
│  └──────────┘ └──────────┘ └──────────────────┘ │
└─────────────────────┬───────────────────────────┘
                      │ Wails Bindings
┌─────────────────────┴───────────────────────────┐
│                  Go Backend                      │
│  ┌──────────┐ ┌──────────┐ ┌──────────────────┐ │
│  │ File     │ │ Device   │ │   Transfer       │ │
│  │ Transfer │ │ Discovery│ │   Manager        │ │
│  │ Service  │ │ Service  │ │   (Queue/Progress)│ │
│  └──────────┘ └──────────┘ └──────────────────┘ │
│  ┌──────────┐ ┌──────────────────────────────┐  │
│  │ Settings │ │     HTTP/TCP Transfer Engine  │  │
│  │ Service  │ │     (Chunked, Resumable)      │  │
│  └──────────┘ └──────────────────────────────┘  │
└─────────────────────────────────────────────────┘
```

## 7. Data Flow

1. User selects files to send via drag-and-drop or file picker
2. Frontend calls `FileTransferService.SendFiles(deviceID, filePaths)`
3. Backend chunks files and initiates HTTP transfer to target device
4. Progress events emitted to frontend in real-time
5. Target device receives chunks, reassembles, writes to disk
6. SHA-256 checksum verified on completion
7. Transfer history updated, notification shown on both devices

## 8. Success Metrics

- Transfer speed achieves 80%+ of network bandwidth
- Zero data corruption (verified by checksum)
- App starts in under 2 seconds
- UI responds within 100ms of user action
- Works on all supported platforms without platform-specific bugs
