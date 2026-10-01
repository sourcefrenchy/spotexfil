# spotify — Mythic C2 Profile

A server-side Mythic C2 profile container that bridges Mythic tasking to the
[spotexfil](https://github.com/sourcefrenchy/spotexfil) Spotify-playlist
transport. The transport is a dumb pipe: agents and this profile exchange
opaque base64 Mythic envelopes (`base64(UUID || blob)`) via encrypted Spotify
playlist descriptions on shared `cmd`/`res` channels.

- Profile definition, parameters, ConfigCheck/OPSECCheck: `C2_Profiles/spotify/spotify/c2functions/`
- Bridge logic (pure, unit-tested, no Mythic/Spotify imports): `C2_Profiles/spotify/spotify/pump/`
- Real adapters (spotexfil client + Mythic gRPC push-C2, with poll-style fallback): `C2_Profiles/spotify/spotify/adapters.go`
- Mythic UI documentation page: `documentation-c2/spotify/_index.md`

See the repository root docs for the transport design and agent-side details.

## Development

```
cd C2_Profiles/spotify
go mod tidy   # replace directive points at ../../../go (the spotexfil module)
go test ./...
```

The `replace github.com/sourcefrenchy/spotexfil => ../../../go` line in
`go.mod` is for local development only; the Dockerfile rewrites it to the
in-context copy at image build time.
