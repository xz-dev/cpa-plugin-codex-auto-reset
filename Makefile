PLUGIN_NAME ?= codex-auto-reset
VERSION ?= 0.1.0
BUILD_DIR ?= dist
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GO_LDFLAGS ?= -s -w -X github.com/xz-dev/cpa-plugin-codex-auto-reset/internal/plugin.pluginVersion=$(VERSION)

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_OUTPUT ?= $(BUILD_DIR)/$(PLUGIN_NAME).$(PLUGIN_EXT)
PLUGIN_HEADER = $(basename $(PLUGIN_OUTPUT)).h
ARCHIVE_NAME ?= $(PLUGIN_NAME)_$(VERSION)_$(GOOS)_$(GOARCH).zip
ARCHIVE_PATH ?= $(BUILD_DIR)/$(ARCHIVE_NAME)
CHECKSUM_PATH ?= $(ARCHIVE_PATH).sha256
CHECKSUMS_PATH ?= $(BUILD_DIR)/checksums.txt

.PHONY: build test vet clean package checksums

build:
	mkdir -p $(dir $(PLUGIN_OUTPUT))
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -buildmode=c-shared -ldflags "$(GO_LDFLAGS)" -o $(PLUGIN_OUTPUT) ./cmd/codex-auto-reset
	rm -f $(PLUGIN_HEADER)

test:
	go test ./...

vet:
	go vet ./...

package: build
	go run ./scripts/package-release -library "$(PLUGIN_OUTPUT)" -archive "$(ARCHIVE_PATH)" -checksum "$(CHECKSUM_PATH)"

checksums: package
	cat $(BUILD_DIR)/*.zip.sha256 | sort -k 2 > "$(CHECKSUMS_PATH)"

clean:
	rm -rf $(BUILD_DIR)
