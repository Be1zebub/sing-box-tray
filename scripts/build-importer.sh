#!/usr/bin/env bash
# Cross-compiles config-importer for Windows from a Linux/macOS/WSL host.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

OUTPUT="build/config-importer.exe"

mkdir -p build
(
	cd config-importer
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
		-ldflags="-s -w" \
		-o "../$OUTPUT" \
		.
)

echo "Built: $OUTPUT"
