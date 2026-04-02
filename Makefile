BINARY=spotexfil
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"
PYTHON=/Library/Developer/CommandLineTools/usr/bin/python3.9

.PHONY: all darwin linux windows clean test test-python test-go lint build

# --- Build targets ---

all: darwin linux windows

darwin:
	cd go && GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o ../dist/$(BINARY)-darwin-arm64 ./cmd/spotexfil
	@echo "Built dist/$(BINARY)-darwin-arm64"

linux:
	cd go && GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o ../dist/$(BINARY)-linux-amd64 ./cmd/spotexfil
	@echo "Built dist/$(BINARY)-linux-amd64"

windows:
	cd go && GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o ../dist/$(BINARY)-windows-amd64.exe ./cmd/spotexfil
	@echo "Built dist/$(BINARY)-windows-amd64.exe"

# --- Test targets ---

test: test-python test-go
	@echo "All tests passed"

test-python:
	cd python && $(PYTHON) -m pytest tests/ -v --tb=short

test-go:
	cd go && go test ./...

# --- Lint ---

lint:
	cd python && flake8 --max-line-length=100 spotexfil/*.py spotexfil/modules/*.py tests/*.py

# --- Clean ---

clean:
	rm -rf dist/
	cd go && go clean
	find . -name __pycache__ -type d -exec rm -rf {} + 2>/dev/null || true
	find . -name '*.pyc' -delete 2>/dev/null || true
