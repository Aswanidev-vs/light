package light

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

func TestLightAssetMatcherPrefersPlatformAwareAsset(t *testing.T) {
	assets := []github.ReleaseAsset{
		{Name: "light-windows-amd64.exe"},
		{Name: "light-windows-amd64-installer.exe"},
		{Name: "light-android-arm64.apk"},
	}

	got := lightAssetMatcher(updater.CheckRequest{Platform: "windows", Arch: "amd64"}, assets)
	if got != 0 {
		t.Fatalf("lightAssetMatcher() = %d, want platform-aware executable at index 0", got)
	}
}

func TestLightAssetMatcherSupportsLegacyReleaseNames(t *testing.T) {
	assets := []github.ReleaseAsset{
		{Name: "light-installer-v1.2.3.exe"},
		{Name: "SHA256SUMS"},
		{Name: "light-v1.2.3.exe"},
	}

	got := lightAssetMatcher(updater.CheckRequest{Platform: "windows", Arch: "amd64"}, assets)
	if got != 2 {
		t.Fatalf("lightAssetMatcher() = %d, want legacy executable at index 2", got)
	}
}
