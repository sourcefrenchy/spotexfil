# spotexfil — Mythic Payload Type

Mythic-side definition of the **spotexfil** agent: the builder, build
parameters, and command set that let operators generate payloads from the
Mythic UI. The agent itself is Go code living in `<repo>/go/cmd/mythicagent`
(module `github.com/sourcefrenchy/spotexfil`); this container compiles it with
`go build` and stamps every configuration value into the binary via
`-ldflags -X` (no config files on disk at runtime).

Pairs with the `spotify` C2 profile (`mythic/C2_Profiles/spotify`).

## Layout

```
Dockerfile   itsafeaturemythic/mythic_python_go base (python3 + go + mythic-container)
main.py      container entrypoint (starts the mythic_container service)
install.sh   stages <repo>/go into ./agent_code before `docker build`
spotexfil/mythic/agent_functions/
  builder.py     PayloadType "spotexfil": params, ldflags stamping, go build
  shell.py download.py upload.py screenshot.py sysinfo.py exit.py
```

## Install / build

```sh
# from this directory: stage the Go agent source, then let mythic-cli build
./install.sh                       # rsyncs ../../../go -> ./agent_code
sudo ./mythic-cli install local <path-to-this-folder>   # or install the whole repo
```

## Build parameters

| parameter    | type      | default                          | notes |
|--------------|-----------|----------------------------------|-------|
| target_os    | ChooseOne | darwin-arm64                     | darwin-arm64 / darwin-amd64 / linux-amd64 / windows-amd64 |
| interval     | Number    | 30                               | poll interval (s); keep >= 20 (Spotify rate limits) |
| jitter       | Number    | 10                               | jitter % (0-100) |
| killdate     | Date      | (empty)                          | optional agent kill date |
| passphrase   | String    | — (required)                     | MUST match the spotify profile's `passphrase` parameter |
| username     | String    | — (required)                     | Spotify account owning the C2 playlists |
| client_id    | String    | — (required)                     | Spotify Developer app client ID |
| client_secret| String    | — (required)                     | Spotify Developer app client secret |
| redirect_uri | String    | http://127.0.0.1:8888/callback   | OAuth redirect URI registered on the app |

The profile's `AESPSK` crypto parameter is wired automatically: the generated
key is stamped into `main.AESKeyB64` (empty when `none` is selected).

## Commands

`shell`, `download`, `upload`, `screenshot`, `sysinfo`, `exit` — tasking is
encoded exactly as the agent expects (`shell` → `{"command": ...}`,
`upload` → `{"path": ..., "content": <base64>}`, `download` → plain path,
`screenshot` → `{"display": n}` or empty).

## OPSEC

Spotify app credentials (client ID/secret) and the transport passphrase are
baked into every payload in recoverable form. Use a **dedicated Spotify
Developer app per campaign** (and a burner Spotify account), so a recovered
binary can't be tied to other operations. See the spotify profile's OPSEC
check output for transport-side warnings.
