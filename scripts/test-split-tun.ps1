# Live split-rule check against a localhost mixed proxy. Does not install TUN
# routes and does not need an administrator. A second sing-box is fine: this
# one only listens on 127.0.0.1:18080 and 127.0.0.1:19090.
#
# What it proves: ip_cidr, domain_suffix and process_path rules from
# split-tun.json send matching proxy connections to the direct outbound, and
# everything else is blackholed. It does not prove route_exclude_address —
# that list only exists on a TUN inbound.
#
# Artifacts: build/split-tun-live/<timestamp>/.
#
#   powershell -ExecutionPolicy Bypass -File scripts\test-split-tun.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\test-split-tun.ps1 -SingBox C:\path\sing-box.exe

param(
	[string]$SingBox = $env:SINGBOX_PATH
)

$ErrorActionPreference = "Stop"
$repo = Split-Path $PSScriptRoot -Parent
Set-Location $repo

function Find-Go {
	$cmd = Get-Command go -ErrorAction SilentlyContinue
	if ($cmd) { return $cmd.Source }
	throw "go.exe not found on PATH"
}

if (-not $SingBox -or -not (Test-Path $SingBox)) {
	throw "sing-box.exe not found. Pass -SingBox or set SINGBOX_PATH."
}

$stampDir = Join-Path $repo "build\split-tun-live"
New-Item -ItemType Directory -Force -Path $stampDir | Out-Null
$log = Join-Path $stampDir "last-run.log"
$art = Join-Path $stampDir (Get-Date -Format "yyyyMMdd-HHmmss")
New-Item -ItemType Directory -Force -Path $art | Out-Null

$go = Find-Go
$env:SINGBOX_PATH = (Resolve-Path $SingBox).Path
$env:SPLIT_TUN_ARTIFACTS = $art
$env:SPLIT_TUN_LIVE_REQUIRED = "1"

Write-Host "sing-box: $env:SINGBOX_PATH"
Write-Host "artifacts: $art"

& $go test -tags live -count=1 -timeout 90s -v ./internal/tun/ -run TestLiveSplitTUN *> $log
$code = $LASTEXITCODE
Set-Content -Path (Join-Path $stampDir "last-exitcode.txt") -Value $code -Encoding ascii
Write-Host "exit $code"
Get-Content $log
exit $code
