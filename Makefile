BINARY=spotexfil
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# -trimpath: strip local build paths from the binary (forensics)
# -buildvcs=false: don't embed VCS metadata
# -s -w: strip symbol table and DWARF debug info
BUILDFLAGS=-trimpath -buildvcs=false
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: all darwin linux windows clean test lint build

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
