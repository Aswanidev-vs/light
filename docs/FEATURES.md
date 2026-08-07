# Light — Feature Tracker

## Status Legend

- **Planned** — Designed but not started
- **In Progress** — Actively being implemented
- **Done** — Implemented and verified
- **Blocked** — Waiting on dependency

---

## Phase 1: Project Setup

### F1.1 Rename Go module
- **Status:** Done
- **Description:** Change `module changeme` to `module light` in go.mod
- **Acceptance:** `go build ./...` succeeds with new module name
- **Dependencies:** None

### F1.2 Update build config
- **Status:** Done
- **Description:** Update `build/config.yml` with Light product metadata
- **Acceptance:** Config reflects correct product name, version, description
- **Dependencies:** None

---

## Phase 2: Go Backend

### F2.1 File Transfer Service (HTTP)
- **Status:** Done
- **Description:** Core service for sending and receiving files via streaming HTTP
- **Acceptance:**
  - Can send a file to a target device
  - Can receive a file from a source device
  - Progress events emitted during transfer
  - SHA-256 checksum verified on completion
  - 32 parallel file transfers
- **Dependencies:** F2.3 (Transfer Manager)

### F2.2 Device Discovery Service
- **Status:** Done
- **Description:** UDP broadcast-based LAN device discovery
- **Acceptance:**
  - Automatically discovers other Light instances on LAN
  - Device list updates in real-time
  - Shows device name, type, and status
- **Dependencies:** None

### F2.3 Transfer Manager
- **Status:** Done
- **Description:** Manages transfer queue, progress tracking, and concurrent transfers
- **Acceptance:**
  - Supports up to 32 concurrent transfers
  - Pause/resume/cancel functionality
  - Transfer history maintained in SQLite
  - Speed and ETA calculated
- **Dependencies:** F2.1

### F2.4 Settings Service
- **Status:** Done
- **Description:** User preferences persistence (download dir, auto-accept, port, theme)
- **Acceptance:**
  - Settings read/write via frontend
  - Settings persist across app restarts
  - Default values sensible
- **Dependencies:** None

### F2.5 QUIC Transport
- **Status:** Done
- **Description:** High-speed QUIC protocol for file transfers with 0-RTT connection
- **Acceptance:**
  - 32 parallel streams
  - 2MB stream buffer
  - Built-in TLS 1.3 encryption
  - No head-of-line blocking
- **Dependencies:** F2.3

### F2.6 SQLite Database
- **Status:** Done
- **Description:** Persistent storage for transfer history and resume data
- **Acceptance:**
  - Transfers persist across app restarts
  - Resume support for interrupted transfers
  - Chunk tracking for partial downloads
- **Dependencies:** F2.3

### F2.7 PIN Protection
- **Status:** Done
- **Description:** Optional PIN for file transfers
- **Acceptance:**
  - Sender can set PIN for transfers
  - Receiver must verify PIN before receiving
  - PIN stored securely in database
- **Dependencies:** F2.5, F2.6

---

## Phase 3: Frontend Architecture

### F3.1 TypeScript Types
- **Status:** Done
- **Description:** Define all TypeScript interfaces (Device, File, Transfer, Settings)
- **Acceptance:**
  - All types exported from `types/index.ts`
  - Types match Go struct definitions
- **Dependencies:** None

### F3.2 Composables
- **Status:** Done
- **Description:** Vue composables for device discovery, file transfer, and settings state
- **Acceptance:**
  - `useDeviceDiscovery` returns reactive device list
  - `useFileTransfer` provides send/receive/pause/cancel functions
  - `useSettings` provides reactive settings state
- **Dependencies:** F3.1, F2.5

### F3.3 SVG Icon Components
- **Status:** Done
- **Description:** Inline SVG icon set (send, receive, device, file, progress, settings, history, status)
- **Acceptance:**
  - All icons render correctly
  - No emojis used anywhere
  - Icons accept size and color props
- **Dependencies:** None

---

## Phase 4: Frontend Components

### F4.1 App Layout
- **Status:** Done
- **Description:** Root layout with sidebar + main content area, responsive for mobile
- **Acceptance:**
  - Desktop: sidebar (300px) + main area
  - Mobile: full-width with bottom tab navigation
  - Smooth transitions between views
- **Dependencies:** F3.3

### F4.2 Device List
- **Status:** Done
- **Description:** Sidebar showing discovered devices with status indicators
- **Acceptance:**
  - Lists all discovered devices
  - Shows device name, type icon, connection status
  - Click to select device for transfer
  - Empty state when no devices found
- **Dependencies:** F3.2, F4.1

### F4.3 Transfer Area
- **Status:** Done
- **Description:** Main content area with drag-and-drop zone and active transfer list
- **Acceptance:**
  - Drag-and-drop files onto the area
  - File picker button as alternative
  - Shows active transfers with progress bars
  - Shows completed transfers
- **Dependencies:** F3.2, F4.1

### F4.4 File Card
- **Status:** Done
- **Description:** Individual file display in transfer queue with name, size, progress
- **Acceptance:**
  - Shows file name and size
  - Progress bar with percentage
  - Transfer speed display
  - Cancel button
- **Dependencies:** F4.3

### F4.5 Progress Bar
- **Status:** Done
- **Description:** Animated progress indicator with speed badge
- **Acceptance:**
  - Smooth animation (CSS transition)
  - Shows percentage text
  - Color changes at milestones (25%, 50%, 75%, 100%)
- **Dependencies:** None

### F4.6 Settings Panel
- **Status:** Done
- **Description:** Settings drawer/panel with all configuration options
- **Acceptance:**
  - Download directory picker
  - Auto-accept toggle
  - Port configuration
  - Device name input
  - Theme toggle
- **Dependencies:** F3.2

### F4.7 Transfer History
- **Status:** Done
- **Description:** List of completed/failed transfers with details
- **Acceptance:**
  - Shows transfer date, files, size, status
  - Click to open file location
  - Clear history option
- **Dependencies:** F3.2

---

## Phase 5: Styling

### F5.1 Global Theme
- **Status:** Done
- **Description:** Industrial utilitarian CSS theme (charcoal base, amber accent)
- **Acceptance:**
  - CSS custom properties for all tokens
  - Dark theme as default
  - Typography: JetBrains Mono + DM Sans
  - No generic AI aesthetics
- **Dependencies:** None

### F5.2 Component Styles
- **Status:** Done
- **Description:** Scoped styles for all components
- **Acceptance:**
  - Cards with subtle borders
  - Hover/active states
  - Transitions 150-200ms ease-out
  - Responsive at 768px breakpoint
- **Dependencies:** F5.1, F4.1-F4.7

---

## Phase 6: CI/CD

### F6.1 GitHub Actions Build Workflow
- **Status:** Done
- **Description:** `.github/workflows/build.yml` for Windows + Android builds
- **Acceptance:**
  - Windows: builds exe + NSIS installer
  - Android: builds fat APK
  - Release job creates GitHub Release on version tags
  - All artifacts uploaded with 30-day retention
- **Dependencies:** None

---

## Phase 7: Performance Improvements

### F7.1 QUIC Protocol
- **Status:** Done
- **Description:** Replace TCP with QUIC for 20-40% faster transfers
- **Acceptance:**
  - 0-RTT connection establishment
  - 32 parallel streams
  - Built-in TLS 1.3 encryption
  - No head-of-line blocking
- **Dependencies:** None

### F7.2 SQLite Persistence
- **Status:** Done
- **Description:** Persistent transfer history and resume support
- **Acceptance:**
  - Transfers survive app restart
  - Resume interrupted transfers
  - Chunk tracking for partial downloads
- **Dependencies:** None

### F7.3 PIN Protection
- **Status:** Done
- **Description:** Optional PIN for secure file transfers
- **Acceptance:**
  - Sender can set PIN
  - Receiver must verify PIN
  - PIN stored in SQLite
- **Dependencies:** F7.2

### F7.4 Increased Parallelism
- **Status:** Done
- **Description:** Increase concurrent transfers from 4 to 32
- **Acceptance:**
  - 32 simultaneous file transfers
  - 32 QUIC streams per connection
  - 2MB buffer sizes
- **Dependencies:** None

---

## Implementation Order Complete

All phases completed. The app now supports:
- High-speed QUIC transfers with 0-RTT
- 32 parallel streams/transfers
- SQLite persistence for history and resume
- PIN protection for secure transfers
- Industrial utilitarian UI with no emojis
- GitHub Actions CI/CD for Windows + Android
