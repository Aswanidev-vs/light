package light

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	defaultPort   = 9120
	discoveryPort = 9129
)

func configDir() string {
	// On Android, os.UserHomeDir() and os.Getwd() return non-writable paths
	// (often "/" or "."), so raw filesystem writes fail. Java writes the app's
	// internal files directory to a marker file during bridge init — read it
	// first for a guaranteed-writable staging path.
	if runtime.GOOS == "android" {
		if marker := readAndroidStagingMarker(); marker != "" {
			dir := filepath.Join(marker, ".light")
			if mkErr := os.MkdirAll(dir, 0o755); mkErr == nil {
				return dir
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "." {
		// On Android, os.UserHomeDir() may fail or return "." (root), which is
		// not writable. Fall back to the working directory — on Android, the Go
		// library runs in the app process where CWD is the data directory.
		if cwd, cwdErr := os.Getwd(); cwdErr == nil && cwd != "" && cwd != "/" {
			home = cwd
		} else {
			home = "."
		}
	}
	dir := filepath.Join(home, ".light")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		// Last resort: try the OS temp directory.
		dir = filepath.Join(os.TempDir(), ".light")
		_ = os.MkdirAll(dir, 0o755)
	}
	return dir
}

// readAndroidStagingMarker reads the staging path marker written by Java's
// WailsBridge.writeStagingMarker() during app initialization. Returns empty
// string if unavailable.
func readAndroidStagingMarker() string {
	// The marker is at <filesDir>/.light/staging_path. We don't know filesDir
	// upfront, so scan known Android data roots.
	roots := []string{
		os.Getenv("ANDROID_DATA"), // typically "/data"
		"/data/data",
		"/data/user/0",
	}
	for _, root := range roots {
		if root == "" || root == "/" {
			continue
		}
		// Try common package names
		for _, pkg := range []string{"com.wails.app", "com.wails.app.light"} {
			marker := filepath.Join(root, pkg, "files", ".light", "staging_path")
			if data, err := os.ReadFile(marker); err == nil {
				p := strings.TrimSpace(string(data))
				if p != "" {
					return p
				}
			}
		}
	}
	return ""
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SettingsService owns the persisted app configuration and a stable device ID.
type SettingsService struct {
	app *application.App
	mu  sync.RWMutex
	cfg Settings
	id  string
}

func NewSettingsService(app *application.App) *SettingsService {
	s := &SettingsService{app: app}
	s.load()
	return s
}

func (s *SettingsService) load() {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(configDir(), "settings.json")
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.cfg)
	}
	if s.cfg.DeviceName == "" {
		s.cfg.DeviceName = hostnameName()
	}
	if s.cfg.Port == 0 {
		s.cfg.Port = defaultPort
	}
	if s.cfg.DownloadDir == "" {
		s.cfg.DownloadDir = defaultDownloadDir()
	}
	if s.cfg.Theme == "" {
		s.cfg.Theme = "dark"
	}

	idPath := filepath.Join(configDir(), "deviceid")
	if b, err := os.ReadFile(idPath); err == nil {
		s.id = string(b)
	}
	if s.id == "" {
		s.id = newID()
		_ = os.WriteFile(idPath, []byte(s.id), 0o600)
	}
}

func (s *SettingsService) save() {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(filepath.Join(configDir(), "settings.json"), data, 0o644)
}

func (s *SettingsService) DeviceID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *SettingsService) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *SettingsService) UpdateSettings(s2 Settings) {
	s.mu.Lock()
	s.cfg.DeviceName = s2.DeviceName
	s.cfg.Port = s2.Port
	s.cfg.DownloadDir = s2.DownloadDir
	s.cfg.DownloadDirUri = s2.DownloadDirUri
	s.cfg.AutoAccept = s2.AutoAccept
	s.cfg.Theme = s2.Theme
	s.cfg.EnableEncryption = s2.EnableEncryption
	s.mu.Unlock()
	s.save()
	s.emit()
}

func (s *SettingsService) SetDeviceName(n string)   { s.set(func(c *Settings) { c.DeviceName = n }) }
func (s *SettingsService) SetPort(p int)            { s.set(func(c *Settings) { c.Port = p }) }
func (s *SettingsService) SetDownloadDir(d string)  { s.set(func(c *Settings) { c.DownloadDir = d }) }
func (s *SettingsService) SetDownloadDirUri(u string) { s.set(func(c *Settings) { c.DownloadDirUri = u }) }
func (s *SettingsService) SetAutoAccept(b bool)     { s.set(func(c *Settings) { c.AutoAccept = b }) }
func (s *SettingsService) SetTheme(t string)        { s.set(func(c *Settings) { c.Theme = t }) }

func (s *SettingsService) set(mut func(*Settings)) {
	s.mu.Lock()
	mut(&s.cfg)
	s.mu.Unlock()
	s.save()
	s.emit()
}

func (s *SettingsService) GetDefaultDownloadDir() string { return defaultDownloadDir() }

func (s *SettingsService) emit() {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if s.app != nil {
		s.app.Event.Emit("settings-changed", cfg)
	}
}

func (s *SettingsService) SetApp(app *application.App) { s.app = app }

func hostnameName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "Light Device"
	}
	return h
}

func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "Light"
	}
	return filepath.Join(home, "Downloads", "Light")
}
