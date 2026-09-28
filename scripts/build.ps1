# Builds sing-box-tray natively on Windows (no `make` required).
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

$Output = "build\sing-box-tray.exe"

New-Item -ItemType Directory -Force -Path "build" | Out-Null

$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"

if (-not $env:VERSION) {
	$described = git describe --tags --always --dirty 2>$null
	if ($described) { $env:VERSION = $described.Trim() }
}

$LdFlags = "-H windowsgui -s -w"
if ($env:VERSION) {
	$LdFlags += " -X github.com/Be1zebub/sing-box-tray/internal/version.Version=$env:VERSION"
}

go build -ldflags="$LdFlags" -o $Output .

Write-Host "Built: $Output"
