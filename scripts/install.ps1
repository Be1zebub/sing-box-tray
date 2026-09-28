# Downloads the latest tray release, sing-box, and wintun into one folder.
# Re-running refreshes the binaries and leaves config.json, split-tun.json,
# and configs\ untouched.
param(
    [string]$Dest = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if (-not $Dest) {
    $Dest = Join-Path (Get-Location) "sing-box-tray"
}
$Dest = [System.IO.Path]::GetFullPath($Dest)

$Repo = "Be1zebub/sing-box-tray"
$SingBoxRepo = "SagerNet/sing-box"
# 0.14.1 is the current wintun stable. wintun.net has no release API.
$WintunZip = "https://www.wintun.net/builds/wintun-0.14.1.zip"
$UA = "sing-box-tray-install"

function Save-Url([string]$Url, [string]$OutFile) {
    & curl.exe -fsSL -A $UA -L --retry 3 --max-time 300 -o $OutFile $Url
    if ($LASTEXITCODE -ne 0) {
        throw "download failed ($LASTEXITCODE): $Url"
    }
}

function Get-GhJson([string]$Url) {
    $tmp = New-TemporaryFile
    try {
        Save-Url $Url $tmp.FullName
        return Get-Content -Raw -Path $tmp.FullName | ConvertFrom-Json
    } finally {
        Remove-Item -Force $tmp.FullName -ErrorAction SilentlyContinue
    }
}

function Expand-Zip([string]$Zip, [string]$OutDir) {
    if (Test-Path $OutDir) {
        Remove-Item -Recurse -Force $OutDir
    }
    New-Item -ItemType Directory -Path $OutDir | Out-Null
    Expand-Archive -Path $Zip -DestinationPath $OutDir -Force
}

function Find-File([string]$Root, [string]$Name, [string]$Like) {
    $items = @(Get-ChildItem -Path $Root -Recurse -File -Filter $Name)
    if ($Like) {
        $items = @($items | Where-Object { $_.FullName -like $Like })
    }
    if ($items.Count -eq 0) {
        throw "$Name not found in $Root"
    }
    return $items[0].FullName
}

function Install-Default([string]$Name, [string]$DestPath) {
    if (Test-Path -LiteralPath $DestPath) {
        return
    }
    if ($PSScriptRoot) {
        $local = Join-Path $PSScriptRoot "..\assets\$Name"
        if (Test-Path -LiteralPath $local) {
            Copy-Item -LiteralPath $local -Destination $DestPath
            return
        }
    }
    Save-Url "https://raw.githubusercontent.com/$Repo/main/assets/$Name" $DestPath
}

foreach ($dir in @($Dest, (Join-Path $Dest "configs"), (Join-Path $Dest "deps"))) {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
}

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("sing-box-tray-install-" + [guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    Write-Host "tray: $Repo"
    $client = Get-GhJson "https://api.github.com/repos/$Repo/releases/latest"
    $exes = @($client.assets | Where-Object { $_.name -like "*.exe" })
    if ($exes.Count -eq 0) {
        throw "release $($client.tag_name) has no .exe assets"
    }
    foreach ($asset in $exes) {
        $out = Join-Path $Dest $asset.name
        Write-Host "  $($asset.name)"
        Save-Url $asset.browser_download_url $out
    }
    Write-Host "sing-box: $SingBoxRepo"
    $sing = Get-GhJson "https://api.github.com/repos/$SingBoxRepo/releases/latest"
    $ver = $sing.tag_name.TrimStart("v")
    $zipName = "sing-box-$ver-windows-amd64.zip"
    $zipAsset = $sing.assets | Where-Object { $_.name -eq $zipName } | Select-Object -First 1
    if (-not $zipAsset) {
        throw "asset $zipName not found in $($sing.tag_name)"
    }
    $zipPath = Join-Path $work $zipName
    Write-Host "  $zipName"
    Save-Url $zipAsset.browser_download_url $zipPath
    $unpacked = Join-Path $work "sing-box"
    Expand-Zip $zipPath $unpacked
    Copy-Item (Find-File $unpacked "sing-box.exe" "") (Join-Path $Dest "deps\sing-box.exe") -Force

    Write-Host "wintun: $WintunZip"
    $wintunZipPath = Join-Path $work "wintun.zip"
    Save-Url $WintunZip $wintunZipPath
    $wintunDir = Join-Path $work "wintun"
    Expand-Zip $wintunZipPath $wintunDir
    Copy-Item (Find-File $wintunDir "wintun.dll" "*\amd64\*") (Join-Path $Dest "deps\wintun.dll") -Force
} finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

Install-Default "tray-config.default.json" (Join-Path $Dest "config.json")
Install-Default "split-tun.default.json" (Join-Path $Dest "split-tun.json")

Write-Host ""
Write-Host "Installed to $Dest"
Write-Host "Put a sing-box config in configs\ and start sing-box-tray.exe"
Write-Host "Re-run this script to update binaries. Your configs are left as they are."
