package light

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// QRCodeService generates QR codes for device pairing. The QR encodes a JSON
// connection ticket {id,name,address,port,type} that the peer scans to connect
// (works cross-subnet, where UDP discovery alone cannot reach).
type QRCodeService struct {
	app      *application.App
	settings *SettingsService
	discovery *DiscoveryService
}

func NewQRCodeService(app *application.App, settings *SettingsService, discovery *DiscoveryService) *QRCodeService {
	return &QRCodeService{app: app, settings: settings, discovery: discovery}
}

func (q *QRCodeService) SetApp(app *application.App) { q.app = app }

func (q *QRCodeService) ticket() string {
	cfg := q.settings.GetSettings()
	addr := q.discovery.LocalEndpoint()
	t := map[string]string{
		"id":      q.settings.DeviceID(),
		"name":    cfg.DeviceName,
		"address": addr,
		"port":    fmt.Sprintf("%d", cfg.Port),
		"type":    string(q.discovery.selfType),
	}
	b, _ := json.Marshal(t)
	return string(b)
}

func (q *QRCodeService) GetDeviceQRCode() string {
	return q.GenerateQRCodeFromText(q.ticket())
}

func (q *QRCodeService) GetDevicePairingCode() string {
	return q.discovery.GetDevicePairingCode()
}

func (q *QRCodeService) GenerateDeviceQRCode(name, id, address, port string, dtype DeviceType) string {
	t := map[string]string{
		"id":      id,
		"name":    name,
		"address": fmt.Sprintf("%s:%s", address, port),
		"port":    port,
		"type":    string(dtype),
	}
	b, _ := json.Marshal(t)
	return q.GenerateQRCodeFromText(string(b))
}

func (q *QRCodeService) GenerateQRCodeFromText(text string) string {
	png, err := qrcode.Encode(text, qrcode.Medium, 256)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}
