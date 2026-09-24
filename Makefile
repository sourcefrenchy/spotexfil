BINARY=spotexfil
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# -trimpath: strip local build paths from the binary (forensics)
# -buildvcs=false: don't embed VCS metadata
# -s -w: strip symbol table and DWARF debug info
BUILDFLAGS=-trimpath -buildvcs=false
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

# --- Obfuscated build (opsec) ---
# garble scrambles package paths/function names and encrypts string
# literals. protocol.json is sanitized per-seed beforehand because
# go:embed data is not a source literal (garble can't touch it) — the
# stock cover names, artists, and crypto labels would otherwise survive
# in the binary. The original protocol.json is restored on exit.
# NOTE: operator and implants MUST come from the same obfuscated build —
# labels differ from stock builds and from other seeds.
SEED ?= $(shell openssl rand -hex 4)
# garble -seed must be base64 decoding to >=8 bytes (SEED is 8 hex chars)
GARBLE_SEED = $(shell printf '%s' '$(SEED)' | openssl base64)
# GOTOOLCHAIN=local: garble cannot patch a toolchain living in the module
# cache. garble v0.17.0 is pinned for go 1.26 (v0.18+ needs go 1.27).
# garble must run as a HOST binary (go run would inherit GOOS and fail to
# execute); `make obfuscated` auto-installs it to GOPATH/bin.
GARBLE = $(shell go env GOPATH)/bin/garble
PROTO = go/internal/shared/protocol.json

.PHONY: all darwin linux windows clean test lint build obfuscated garble-install

# --- Build targets ---

all: darwin linux windows

darwin:
	cd go && GOOS=darwin GOARCH=arm64 go build $(BUILDFLAGS) $(LDFLAGS) -o ../dist/$(BINARY)-darwin-arm64 ./cmd/spotexfil
	@echo "Built dist/$(BINARY)-darwin-arm64"

linux:
	cd go && GOOS=linux GOARCH=amd64 go build $(BUILDFLAGS) $(LDFLAGS) -o ../dist/$(BINARY)-linux-amd64 ./cmd/spotexfil
	@echo "Built dist/$(BINARY)-linux-amd64"

windows:
	cd go && GOOS=windows GOARCH=amd64 go build $(BUILDFLAGS) $(LDFLAGS) -o ../dist/$(BINARY)-windows-amd64.exe ./cmd/spotexfil
	@echo "Built dist/$(BINARY)-windows-amd64.exe"

garble-install:
	@test -x $(GARBLE) || GOTOOLCHAIN=local go install mvdan.cc/garble@v0.17.0

obfuscated: garble-install
	@echo "[*] Obfuscated build (seed=$(SEED))"
	@set -e; \
	cp $(PROTO) $(PROTO).bak; \
	trap 'mv -f $(CURDIR)/$(PROTO).bak $(CURDIR)/$(PROTO)' EXIT; \
	python3 scripts/sanitize_protocol.py $(PROTO) '$(SEED)'; \
	cd go && GOOS=darwin GOARCH=arm64 $(GARBLE) -seed=$(GARBLE_SEED) -literals build $(BUILDFLAGS) -ldflags "-s -w -X main.version=$(VERSION)-obf" -o ../dist/$(BINARY)-obf-darwin-arm64 ./cmd/spotexfil; \
	echo "Built dist/$(BINARY)-obf-darwin-arm64"; \
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GARBLE) -seed=$(GARBLE_SEED) -literals build $(BUILDFLAGS) -ldflags "-s -w -X main.version=$(VERSION)-obf" -o ../dist/$(BINARY)-obf-linux-amd64 ./cmd/spotexfil; \
	echo "Built dist/$(BINARY)-obf-linux-amd64"; \
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GARBLE) -seed=$(GARBLE_SEED) -literals build $(BUILDFLAGS) -ldflags "-s -w -X main.version=$(VERSION)-obf" -o ../dist/$(BINARY)-obf-windows-amd64.exe ./cmd/spotexfil; \
	echo "Built dist/$(BINARY)-obf-windows-amd64.exe"; \
	echo "[!] Operator + implants must ALL come from this build (labels differ from stock)"

# --- Test targets ---

test:
	cd go && go test -race ./...

# --- Lint ---

lint:
	cd go && go vet ./...

# --- Clean ---

clean:
	rm -rf dist/
	cd go && go clean
