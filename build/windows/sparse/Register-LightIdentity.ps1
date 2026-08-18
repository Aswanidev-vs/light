# Register-LightIdentity.ps1
#
# Registers (or removes) Light's identity (sparse) package so the unpackaged
# light.exe is granted the `proximity` device capability that Wi-Fi Direct
# (Windows.Devices.WiFiDirect) requires on Windows.
#
# This file ships inside the installer as {app}\sparse\ and is run post-install.
# Only two small text files are embedded (AppxManifest.xml + this script).
# MakeAppx.exe is NOT bundled: if it exists on the USER'S machine it is used to
# build the .msix at install time; otherwise the manifest is registered directly
# (loose registration) which needs no MakeAppx at all.
#
# Usage (from the folder containing AppxManifest.xml, or pass -InstallDir):
#   .\Register-LightIdentity.ps1 -InstallDir "C:\Program Files\Light"   # register
#   .\Register-LightIdentity.ps1 -Unregister                             # remove
#
# Windows 10 build 19041+ is required (the AllowExternalContent feature).
# Because the package is UNSIGNED (no code-signing certificate), registration
# requires Windows Developer Mode (free). With a trusted signing certificate it
# would register for any user without Developer Mode.

param(
  [string]$InstallDir = $PSScriptRoot,
  [switch]$Unregister
)

$ErrorActionPreference = 'Stop'
$packageName = 'Light'
$manifest    = Join-Path $PSScriptRoot 'AppxManifest.xml'
$msix        = Join-Path $PSScriptRoot 'Light.identity.msix'

function Test-DeveloperMode {
  try {
    $v = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock' `
      -Name 'AllowDevelopmentWithoutDevLicense' -ErrorAction SilentlyContinue
    return ($v.AllowDevelopmentWithoutDevLicense -eq 1)
  } catch { return $false }
}

# Locate MakeAppx.exe on the user's machine. PATH first, then the Windows SDK
# install folders (sorted newest-first). Returns $null if not installed.
function Find-MakeAppx {
  $cmd = Get-Command MakeAppx.exe -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  $root = 'C:\Program Files (x86)\Windows Kits\10\bin'
  if (Test-Path $root) {
    $verDirs = Get-ChildItem $root -Directory -Filter '10.*' |
      Sort-Object { [version]$_.Name } -Descending
    foreach ($ver in $verDirs) {
      foreach ($arch in @('x64','x86','arm64')) {
        $p = Join-Path $ver.FullName (Join-Path $arch 'makeappx.exe')
        if (Test-Path $p) { return $p }
      }
    }
  }
  return $null
}

function Remove-ExistingPackage {
  # Re-registering the same version fails with 0x80073CF9; remove it first.
  Get-AppxPackage -Name $packageName -ErrorAction SilentlyContinue |
    Remove-AppxPackage -ErrorAction SilentlyContinue
}

if ($Unregister) {
  Remove-ExistingPackage
  Write-Host "Light identity package unregistered (if present)."
  exit 0
}

if (-not (Test-Path $manifest)) {
  Write-Warning "AppxManifest.xml not found next to this script; skipping Wi-Fi Direct capability."
  exit 0
}

Remove-ExistingPackage

$devMode  = Test-DeveloperMode
$makeappx = Find-MakeAppx

try {
  if ($makeappx) {
    # Build the .msix with the user's SDK MakeAppx, then register it.
    if (Test-Path $msix) { Remove-Item $msix -Force }
    Write-Host "Building identity package with $makeappx ..."
    & $makeappx pack /o /d $PSScriptRoot /nv /p $msix | Out-Null
    Write-Host "Registering identity package: $msix"
    Add-AppxPackage -Path $msix -ExternalLocation $InstallDir
  } else {
    # No MakeAppx on this machine: register the manifest directly (loose
    # registration). No .msix build needed.
    Write-Host "MakeAppx not found; registering identity package from manifest (loose) ..."
    Add-AppxPackage -Register $PSScriptRoot -ExternalLocation $InstallDir
  }
  Write-Host "OK: Light identity package registered. Wi-Fi Direct (proximity) should be available."
}
catch {
  Write-Warning "Could not register Light identity package: $_"
  if (-not $devMode) {
    Write-Warning ("The package is unsigned, so Windows Developer Mode is required. " +
                   "Enable it: Settings > System > For developers. The app still works over the LAN.")
  } else {
    Write-Warning ("Wi-Fi Direct on Windows needs the `proximity` capability. The app still " +
                   "works over the LAN (same Wi-Fi) path.")
  }
  # Exit 0 so the Inno Setup install is never blocked by this optional step.
  exit 0
}
