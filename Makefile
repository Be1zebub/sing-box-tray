.PHONY: build build-importer build-all clean

BUILD_DIR := build
OUTPUT    := $(BUILD_DIR)/sing-box-tray.exe
IMPORTER  := $(BUILD_DIR)/config-importer.exe

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)

build:
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
		-ldflags="-H windowsgui -s -w -X github.com/Be1zebub/sing-box-tray/internal/version.Version=$(VERSION)" \
		-o $(OUTPUT) \
		.
	@echo "Built: $(OUTPUT)"

build-importer:
	@mkdir -p $(BUILD_DIR)
	cd config-importer && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
		-ldflags="-s -w" \
		-o ../$(IMPORTER) \
		.
	@echo "Built: $(IMPORTER)"

build-all: build build-importer

clean:
	rm -rf $(BUILD_DIR)
