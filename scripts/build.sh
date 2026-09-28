#!/usr/bin/env bash
# Cross-compiles sing-box-tray for Windows from a Linux/macOS/WSL host.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

OUTPUT="build/sing-box-tray.exe"

if [ -z "${VERSION:-}" ]; then
	VERSION="$(git describe --tags --always --dirty 2>/dev/null || true)"
fi

LDFLAGS="-H windowsgui -s -w"
if [ -n "${VERSION:-}" ]; then
	LDFLAGS="$LDFLAGS -X github.com/Be1zebub/sing-box-tray/internal/version.Version=$VERSION"
fi

mkdir -p build
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
	-ldflags="$LDFLAGS" \
	-o "$OUTPUT" \
	.

echo "Built: $OUTPUT"
