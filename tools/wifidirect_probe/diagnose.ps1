# Quick diagnostic: report OS build + Developer Mode from a plain PowerShell.
Write-Host "OS build: $([System.Environment]::OSVersion.Version.Build)"
Write-Host "Developer Mode (HKLM AppModelUnlock):"
$dm = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock' -ErrorAction SilentlyContinue
if ($dm) { Write-Host ("  AllowDevelopmentWithoutDevLicense = " + ($dm.AllowDevelopmentWithoutDevLicense ?? '<not set>')) }
else { Write-Host "  key not found" }
