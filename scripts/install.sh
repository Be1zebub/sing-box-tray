#!/usr/bin/env bash
# Downloads the latest tray release, sing-box, and wintun into one folder.
# Re-running refreshes the binaries and leaves config.json, split-tun.json,
# and configs/ untouched.
set -euo pipefail

DEST="${1:-$(pwd)/sing-box-tray}"
DEST="$(cd "$(dirname "$DEST")" && pwd)/$(basename "$DEST")"

REPO="Be1zebub/sing-box-tray"
SINGBOX_REPO="SagerNet/sing-box"
# 0.14.1 is the current wintun stable. wintun.net has no release API.
WINTUN_ZIP="https://www.wintun.net/builds/wintun-0.14.1.zip"
UA="sing-box-tray-install"

# Checkout next to this script. Empty when the script is piped to bash.
ROOT=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
	ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi

mkdir -p "$DEST/configs" "$DEST/deps"

WORK="$(mktemp -d)"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

save_url() {
	curl -fsSL -A "$UA" -L --retry 3 --max-time 300 -o "$2" "$1"
}

extract_zip() {
	local zip="$1" out="$2"
	mkdir -p "$out"
	if command -v unzip >/dev/null 2>&1; then
		unzip -q "$zip" -d "$out"
	else
		tar -xf "$zip" -C "$out"
	fi
}

echo "tray: $REPO"
client_json="$(curl -fsSL -A "$UA" -L --max-time 60 "https://api.github.com/repos/$REPO/releases/latest")"
mapfile -t exe_urls < <(printf '%s\n' "$client_json" | grep -oE 'https://[^"]+\.exe' | awk '!seen[$0]++' || true)
if [[ ${#exe_urls[@]} -eq 0 ]]; then
	echo "latest release has no .exe assets" >&2
	exit 1
fi
for url in "${exe_urls[@]}"; do
	name="$(basename "$url")"
	echo "  $name"
	save_url "$url" "$DEST/$name"
done

echo "sing-box: $SINGBOX_REPO"
sing_json="$(curl -fsSL -A "$UA" -L --max-time 60 "https://api.github.com/repos/$SINGBOX_REPO/releases/latest")"
mapfile -t sing_urls < <(printf '%s\n' "$sing_json" | grep -oE 'https://[^"]+/sing-box-[0-9.]+-windows-amd64\.zip' || true)
sing_url="${sing_urls[0]:-}"
if [[ -z "$sing_url" ]]; then
	echo "windows amd64 sing-box zip not found" >&2
	exit 1
fi
echo "  $(basename "$sing_url")"
save_url "$sing_url" "$WORK/sing-box.zip"
extract_zip "$WORK/sing-box.zip" "$WORK/sing-box"
sing_exe="$(find "$WORK/sing-box" -type f -name 'sing-box.exe' -print -quit)"
if [[ -z "$sing_exe" ]]; then
	echo "sing-box.exe not found in the archive" >&2
	exit 1
fi
cp -f "$sing_exe" "$DEST/deps/sing-box.exe"

echo "wintun: $WINTUN_ZIP"
save_url "$WINTUN_ZIP" "$WORK/wintun.zip"
extract_zip "$WORK/wintun.zip" "$WORK/wintun"
wintun_dll="$(find "$WORK/wintun" -type f -path '*/amd64/wintun.dll' -print -quit)"
if [[ -z "$wintun_dll" ]]; then
	echo "amd64/wintun.dll not found in the archive" >&2
	exit 1
fi
cp -f "$wintun_dll" "$DEST/deps/wintun.dll"

install_default() {
	local name="$1" dest="$2"
	if [[ -e "$dest" ]]; then
		return
	fi
	if [[ -n "$ROOT" && -f "$ROOT/assets/$name" ]]; then
		cp "$ROOT/assets/$name" "$dest"
		return
	fi
	save_url "https://raw.githubusercontent.com/$REPO/main/assets/$name" "$dest"
}

install_default tray-config.default.json "$DEST/config.json"
install_default split-tun.default.json "$DEST/split-tun.json"

echo
echo "Installed to $DEST"
echo "Put a sing-box config in configs/ and start sing-box-tray.exe"
echo "Re-run this script to update binaries. Your configs are left as they are."
