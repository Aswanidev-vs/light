package light

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

var errUpdatesUnavailable = errors.New("updates are unavailable until the application has started")
var errDirectUpdateUnavailable = errors.New("direct updates are not supported on this platform")

// UpdateInfo is the UI-safe subset of a GitHub release. The full release is
// kept by Wails' updater so DownloadAndInstall can reuse its verified artifact.
type UpdateInfo struct {
	Supported      bool   `json:"supported"`
	CanInstall     bool   `json:"canInstall"`
	CurrentVersion string `json:"currentVersion"`
	Available      bool   `json:"available"`
	Version        string `json:"version,omitempty"`
	Name           string `json:"name,omitempty"`
	Notes          string `json:"notes,omitempty"`
	ReleaseURL     string `json:"releaseUrl,omitempty"`
	PublishedAt    string `json:"publishedAt,omitempty"`
	ArtifactSize   int64  `json:"artifactSize,omitempty"`
}

// UpdateService adapts the Wails updater to Light's main window. A separate
// service keeps the generated frontend API small and lets Android show release
// information without attempting an impossible in-place APK replacement.
type UpdateService struct {
	app     *application.App
	mu      sync.Mutex
	initErr error
}

func NewUpdateService(app *application.App) *UpdateService {
	return &UpdateService{app: app}
}

// SetApp configures the Wails GitHub provider once the application exists.
func (s *UpdateService) SetApp(app *application.App) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.app = app
	s.initErr = nil

	if app == nil || !updateCheckSupported() {
		return
	}

	provider, err := github.New(github.Config{
		Repository:    updateRepository,
		ChecksumAsset: "SHA256SUMS",
		AssetMatcher:  lightAssetMatcher,
	})
	if err != nil {
		s.initErr = err
		return
	}

	s.initErr = app.Updater.Init(updater.Config{
		CurrentVersion: AppVersion,
		Providers:      []updater.Provider{provider},
		Window:         updater.WindowNone,
	})
}

func (s *UpdateService) CurrentVersion() string { return AppVersion }

func (s *UpdateService) IsSupported() bool { return updateCheckSupported() }

func (s *UpdateService) CanInstall() bool { return directUpdateSupported() }

// CheckForUpdates checks the stable GitHub release channel. The updater emits
// its lifecycle events, while this return value is convenient for first paint.
func (s *UpdateService) CheckForUpdates() (UpdateInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !updateCheckSupported() {
		return s.baseInfo(), nil
	}
	if s.app == nil || s.app.Updater == nil {
		return UpdateInfo{}, errUpdatesUnavailable
	}
	if s.initErr != nil {
		return UpdateInfo{}, fmt.Errorf("initialize updater: %w", s.initErr)
	}

	release, err := s.app.Updater.Check(context.Background())
	if err != nil {
		return UpdateInfo{}, err
	}
	if release == nil {
		return s.baseInfo(), nil
	}
	return s.releaseInfo(release), nil
}

// DownloadAndInstall downloads the release found by CheckForUpdates and
// stages it after Wails verifies its checksum.
func (s *UpdateService) DownloadAndInstall() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !directUpdateSupported() {
		return errDirectUpdateUnavailable
	}
	if s.app == nil || s.app.Updater == nil {
		return errUpdatesUnavailable
	}
	if s.initErr != nil {
		return fmt.Errorf("initialize updater: %w", s.initErr)
	}
	return s.app.Updater.DownloadAndInstall(context.Background())
}

// Restart applies the staged artifact through Wails' detached helper and
// relaunches the updated executable.
func (s *UpdateService) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !directUpdateSupported() {
		return errDirectUpdateUnavailable
	}
	if s.app == nil || s.app.Updater == nil {
		return errUpdatesUnavailable
	}
	return s.app.Updater.Restart(context.Background())
}

func (s *UpdateService) baseInfo() UpdateInfo {
	return UpdateInfo{
		Supported:      updateCheckSupported(),
		CanInstall:     directUpdateSupported(),
		CurrentVersion: AppVersion,
	}
}

func (s *UpdateService) releaseInfo(release *updater.Release) UpdateInfo {
	info := s.baseInfo()
	info.Available = true
	info.Version = release.Version
	info.Name = release.Name
	info.Notes = release.Notes
	info.PublishedAt = release.PublishedAt.Format("2006-01-02T15:04:05Z07:00")
	info.ArtifactSize = release.Artifact.Size
	if release.Metadata != nil {
		if url, ok := release.Metadata["github.release.htmlURL"].(string); ok {
			info.ReleaseURL = url
		}
	}
	return info
}

func updateCheckSupported() bool {
	switch runtime.GOOS {
	case "windows", "darwin", "linux", "android":
		return true
	default:
		return false
	}
}

func directUpdateSupported() bool {
	switch runtime.GOOS {
	case "windows", "darwin", "linux":
		return true
	default:
		return false
	}
}

// lightAssetMatcher supports both the new platform-aware names and the
// existing light-vX.Y.Z.* assets already published by the repository.
func lightAssetMatcher(req updater.CheckRequest, assets []github.ReleaseAsset) int {
	if index := github.DefaultAssetMatcher(req, assets); index >= 0 {
		return index
	}

	for i, asset := range assets {
		name := strings.ToLower(asset.Name)
		if isReleaseSidecar(name) || isInstaller(name) {
			continue
		}
		switch req.Platform {
		case "windows":
			if strings.HasSuffix(name, ".exe") {
				return i
			}
		case "android":
			if strings.HasSuffix(name, ".apk") {
				return i
			}
		case "darwin":
			if strings.HasSuffix(name, ".dmg") || strings.HasSuffix(name, ".zip") {
				return i
			}
		case "linux":
			if strings.HasSuffix(name, ".appimage") || strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".tar.gz") {
				return i
			}
		}
	}
	return -1
}

func isInstaller(name string) bool {
	return strings.Contains(name, "-installer") || strings.Contains(name, "_installer") || name == "installer.exe"
}

func isReleaseSidecar(name string) bool {
	return strings.HasSuffix(name, ".sig") || strings.HasSuffix(name, ".asc") ||
		strings.HasSuffix(name, ".sha256") || strings.HasSuffix(name, ".sha512") ||
		strings.HasSuffix(name, ".sums") || strings.HasSuffix(name, ".checksums") ||
		name == "sha256sums" || strings.HasPrefix(name, "sha256sums.")
}
