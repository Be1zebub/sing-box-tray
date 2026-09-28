# Builds config-importer natively on Windows (no `make` required).
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

$Output = "build\config-importer.exe"

New-Item -ItemType Directory -Force -Path "build" | Out-Null

$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"

Push-Location "config-importer"
try {
	go build -ldflags="-s -w" -o "..\$Output" .
} finally {
	Pop-Location
}

Write-Host "Built: $Output"
