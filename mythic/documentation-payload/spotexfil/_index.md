# spotexfil

Cross-platform Mythic agent (macOS arm64/amd64, Linux amd64, Windows amd64) that
communicates exclusively through the **spotify** C2 profile — encrypted tasking
hidden in private Spotify playlist descriptions.

## Summary

- **Transport**: Spotify playlists via the `spotify` C2 profile (see C2 Profiles docs)
- **Crypto**: Mythic AESPSK (`aes256_hmac`) or plaintext, per payload configuration
- **Commands**: `shell`, `download`, `upload`, `screenshot`, `sysinfo`, `exit`
- **Build**: Go, cross-compiled per payload; `-tags implantonly` strips the operator console

## Opsec notes

- The agent never runs interactive OAuth and never writes a token cache — pre-stage
  the Spotify token on the target (`SPOTIFY_TOKEN_JSON` env var or token file).
- Spotify app credentials are stamped into the binary at build time — use a
  **dedicated Spotify developer app per campaign** and treat the binary as sensitive.
- Commands execute with `HISTFILE=/dev/null HISTSIZE=0`.
