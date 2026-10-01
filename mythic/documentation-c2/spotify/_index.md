+++
title = "spotify"
chapter = false
weight = 5
+++

## Overview

The `spotify` C2 profile tunnels Mythic tasking through Spotify playlist
metadata using the [spotexfil](https://github.com/sourcefrenchy/spotexfil)
transport. Neither the agent nor the profile ever talks to Spotify's Web API
with Mythic-shaped traffic: both sides read and write encrypted playlist
descriptions on two shared channels.

The profile is **server-routed** (`IsServerRouted`): agents never connect to
the Mythic server. The profile container polls the agent response channel,
forwards envelopes to Mythic over the push-C2 gRPC stream, and writes tasking
back to the command channel. Envelopes are opaque
`base64(UUID || encrypted-blob)` blobs; the profile only reads the cleartext
36-char UUID prefix to correlate messages.

```
                        Spotify Web API
        ┌───────────────────────────────────────────┐
        │   "cmd" playlists      "res" playlists    │
        │   (profile writes)     (agents write)     │
        └───────▲───────────────────────▲───────────┘
                │ poll/write            │ poll/write
   ┌────────────┴─────────┐   ┌─────────┴────────────┐
   │  spotify C2 profile  │   │  agent (implant)     │
   │  container (pump)    │   │  filters by UUID     │
   └────────────▲─────────┘   └──────────────────────┘
                │ gRPC push-C2 (StartPushC2StreamingOneToMany)
        ┌───────┴────────┐
        │  Mythic server │
        └────────────────┘
```

## Parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `AESPSK` | ChooseOne (crypto) | `aes256_hmac` | Mythic message crypto (`aes256_hmac` or `none`). Key generated per-payload by Mythic. |
| `callback_host` | String | `https://open.spotify.com` | Unused by this server-routed profile; kept for build convention. |
| `spotify_username` | String | — | Spotify account owning the C2 playlists. |
| `spotify_client_id` | String | — | Spotify Developer app client ID. |
| `spotify_client_secret` | String | — | Spotify Developer app client secret. |
| `spotify_redirect_uri` | String | `http://127.0.0.1:8888/callback` | OAuth redirect URI (only for interactive token acquisition). |
| `spotify_token_file` | String | — | Path to a spotipy-format token JSON for headless auth. |
| `passphrase` | String | — | Transport passphrase. **Must exactly match the agent build.** |
| `poll_interval` | Number | `30` | Seconds between response-channel polls (minimum `20`). |

### Container runtime configuration

Mythic does not push C2 parameters into the container, so the running bridge
is configured with environment variables on the container (set these to match
the active payload's parameters):

- `SPOTIFY_USERNAME`, `SPOTIFY_CLIENT_ID`, `SPOTIFY_CLIENT_SECRET`,
  `SPOTIFY_REDIRECTURI` — consumed by the spotexfil client
  (a `.spotexfil.conf` file also works).
- `SPOTIFY_TOKEN_FILE` — pre-staged spotipy token cache (recommended; the
  container runs with interactive OAuth disabled).
- `SPOTEXFIL_PASSPHRASE` — transport passphrase, must match the agent build.
- `SPOTEXFIL_POLL_INTERVAL` — poll interval in seconds (default `30`, min `20`).
- `SPOTEXFIL_POLL_FALLBACK=1` + `MYTHIC_ADDRESS` — use the legacy HTTP POST
  relay instead of the gRPC push-C2 stream.

## OPSEC notes

- **Shared channels.** All agents and the profile use one Spotify account.
  The passphrase protects playlist *contents*, but playlist names, counts,
  and create/update/delete timing are visible to Spotify and to anyone with
  account access. Use a burner account with no link to a real identity or
  payment method.
- **Playlist churn.** Creating and deleting playlists every poll cycle is
  highly anomalous consumer behavior. Keep `poll_interval` at 30s or higher.
- **Passphrase reuse.** Compromise of one agent's passphrase exposes the
  whole channel history on that account. Rotate the account/passphrase per
  operation.
- **Attribution.** The Spotify Developer app (client ID/secret) is tied to a
  developer account; treat it as burnable infrastructure.
