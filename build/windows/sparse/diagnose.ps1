# Diagnostic helper for the Light Wi-Fi Direct identity (sparse) package.
# Run from an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File build\windows\sparse\diagnose.ps1

$packageName = 'Light'
$installDir  = $null

Write-Host "=== 1. OS build (needs >= 19041 for AllowExternalContent) ==="
$ver = [System.Environment]::OSVersion.Version
Write-Host "OS version: $($ver.Major).$($ver.Minor) build $($ver.Build)"

Write-Host "`n=== 2. Developer Mode registry ==="
$dm = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock' -ErrorAction SilentlyContinue
if ($dm) {
    Write-Host ("AllowDevelopmentWithoutDevLicense = " +
        ($dm.AllowDevelopmentWithoutDevLicense ?? '<not set>'))
} else {
    Write-Host "AppModelUnlock key not found (Developer Mode likely OFF)."
}

Write-Host "`n=== 3. MakeAppx.exe on this machine ==="
function Find-MakeAppx {
    $cmd = Get-Command MakeAppx.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $root = 'C:\Program Files (x86)\Windows Kits\10\bin'
    if (Test-Path $root) {
        $verDirs = Get-ChildItem $root -Directory -Filter '10.*' |
            Sort-Object { [version]$_.Name } -Descending
        foreach ($v in $verDirs) {
            foreach ($arch in @('x64','x86','arm64')) {
                $p = Join-Path $v.FullName (Join-Path $arch 'makeappx.exe')
                if (Test-Path $p) { return $p }
            }
        }
    }
    return $null
}
$makeappx = Find-MakeAppx
if ($makeappx) { Write-Host "Found: $makeappx" } else { Write-Host "NOT FOUND" }

Write-Host "`n=== 4. Existing '$packageName' appx package ==="
$pkg = Get-AppxPackage -Name $packageName -ErrorAction SilentlyContinue
if ($pkg) {
    $pkg | Format-List Name,Version,Architecture,PackageFullName,InstallLocation
    $installDir = $pkg.InstallLocation
    Write-Host "ExternalLocation should be the app install directory (e.g. C:\Program Files\Light)."
} else {
    Write-Host "Not registered. ExternalLocation must be supplied; try:"
    Write-Host "   .\Register-LightIdentity.ps1 -InstallDir `"C:\Program Files\Light`""
}

Write-Host "`n=== 5. Registration attempt ($packageName) ==="
if ($makeappx) {
    $msix = Join-Path $PSScriptRoot 'Light.identity.msix'
    Write-Host "Building identity package: $makeappx pack ..."
    & $makeappx pack /o /d $PSScriptRoot /nv /p $msix 2>&1 | Select-Object -Last 3
    if (-not (Test-Path $msix)) { exit 1 }
    $target = $installDir
    if (-not $target) {
        $target = Join-Path $env:LOCALAPPDATA 'Light'   # guess if unknown
    }
    Write-Host "Registering with ExternalLocation = $target"
    try {
        Add-AppxPackage -Path $msix -ExternalLocation $target
        Write-Host "OK: registered."
    } catch {
        Write-Warning "Registration failed: $_"
        Write-Warning "Common causes: (a) unsigned package without Developer Mode, (b) Publisher CN does not match a trusted cert, (c) ExternalLocation missing/wrong."
    }
} else {
    Write-Warning "MakeAppx not found; cannot build .msix here. Install the Windows 10 SDK or re-run on a build machine."
}

Write-Host "`n=== 6. Verify identity from a Go process (optional) ==="
Write-Host "Run: go run ./tools/wifidirect_probe   (should report Package identity: PRESENT and WiFiDirectDevice OK)."
