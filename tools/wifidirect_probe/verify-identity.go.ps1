# After registering the identity package, verify the running Go process now
# reports package identity. Usage:
#   powershell -ExecutionPolicy Bypass -File tools\wifidirect_probe\verify-identity.go.ps1
$src = Join-Path $PSScriptRoot 'main.go'
if (-not (Test-Path $src)) { Write-Warning "main.go next to this file not found"; exit 1 }
Write-Host "Building probe (this runs the same identity check the app does)..."
go run $src
Write-Host ""
Write-Host "Expected after successful registration:"
Write-Host "  Package identity: PRESENT"
Write-Host "  Windows.Devices.WiFiDirect.WiFiDirectDevice  OK"
