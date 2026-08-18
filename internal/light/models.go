package light

import (
	"runtime"
	"time"
)

type DeviceType string

const (
	DeviceTypeDesktop DeviceType = "desktop"
	DeviceTypeMobile  DeviceType = "mobile"
)

// PlatformDeviceType infers desktop vs mobile from the Go runtime OS. On mobile
// builds GOOS is "android" or "ios", so this works without build tags.
func PlatformDeviceType() DeviceType {
	switch runtime.GOOS {
	case "android", "ios":
		return DeviceTypeMobile
	default:
		return DeviceTypeDesktop
	}
}

type Device struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Type     DeviceType `json:"type"`
	Address  string     `json:"address"` // host:port of the peer's transfer server
	Code     string     `json:"code"`    // pairing code last advertised by this peer
	LastSeen time.Time  `json:"lastSeen"`
}

type TransferStatus string

const (
	StatusPending   TransferStatus = "pending"
	StatusActive    TransferStatus = "active"
	StatusPaused    TransferStatus = "paused"
	StatusCompleted TransferStatus = "completed"
	StatusFailed    TransferStatus = "failed"
	StatusCancelled TransferStatus = "cancelled"
)

type Transfer struct {
	ID          string         `json:"id"`
	Filename    string         `json:"filename"`
	Size        int64          `json:"size"`
	Transferred int64          `json:"transferred"`
	Percent     int            `json:"percent"`
	Speed       int64          `json:"speed"` // bytes/sec
	Status      TransferStatus `json:"status"`
	Error       string         `json:"error,omitempty"`
	FilePath    string         `json:"filePath,omitempty"`
	Checksum    string         `json:"checksum,omitempty"`
	StartedAt   time.Time      `json:"startedAt"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
}

type FileManifestEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

type PreparePayload struct {
	TransferID string              `json:"transferId"`
	SenderID   string              `json:"senderId,omitempty"`
	SenderName string              `json:"senderName"`
	SenderAddr string              `json:"senderAddr,omitempty"`
	SenderType DeviceType          `json:"senderType,omitempty"`
	Files      []FileManifestEntry `json:"files"`
}

type TransferRequest struct {
	DeviceID   string   `json:"deviceId"`
	DeviceAddr string   `json:"deviceAddr"`
	FilePaths  []string `json:"filePaths"`
}

type Settings struct {
	DeviceName       string `json:"deviceName"`
	Port             int    `json:"port"`
	DownloadDir      string `json:"downloadDir"`
	DownloadDirUri   string `json:"downloadDirUri"` // Android SAF tree URI for the chosen folder
	AutoAccept       bool   `json:"autoAccept"`
	Theme            string `json:"theme"`
	EnableEncryption bool   `json:"enableEncryption"`
	// TransportMode controls outgoing transport selection. QUIC (HTTP/3) is the
	// default and preferred transport: the client probes the peer over HTTP/3
	// and falls back to TCP automatically before uploading if UDP/QUIC is
	// unavailable or the peer does not support it. Set to "tcp" to force plain
	// TCP for outgoing transfers.
	TransportMode string `json:"transportMode"`
	// WifiDirect, when enabled, attempts to form a Wi-Fi Direct (P2P) group with
	// a peer so transfers run over a private link instead of the shared LAN. It is
	// opt-in and orthogonal to TransportMode; if group formation fails the app
	// falls back to normal LAN discovery. Unsupported on macOS, where the toggle
	// is hidden.
	WifiDirect bool `json:"wifiDirect"`
}
