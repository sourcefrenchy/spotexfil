# Mythic C2 profile container for the "spotify" profile.
#
# mythic-cli builds with THIS directory as the docker context, so the
# spotexfil Go module must be staged here first:
#
#   ./stage.sh          # copies <repo>/go -> ./spotexfil_src
#
# The go.mod `replace` directive (../../../go for local dev) is rewritten
# below to the in-context copy.

FROM golang:1.26-bookworm AS builder

# Copy the spotexfil transport module and the profile container source.
COPY spotexfil_src/ /spotexfil/
COPY go.mod go.sum main.go /build/
COPY spotify/ /build/spotify/

# Point the replace directive at the in-context copy of spotexfil.
WORKDIR /build
RUN sed -i -e 's#=> ../../../go#=> /spotexfil#' go.mod

RUN go mod tidy && CGO_ENABLED=0 go build -o /spotify_server .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates

COPY --from=builder /spotify_server /spotify_server

# Runtime configuration (see main.go):
#   SPOTIFY_USERNAME / SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET
#   SPOTIFY_TOKEN_FILE        (pre-staged spotipy-format token JSON)
#   SPOTEXFIL_PASSPHRASE      (must match the agent build)
#   SPOTEXFIL_POLL_INTERVAL   (seconds, >= 20)
#   MYTHIC_SERVER_HOST / MYTHIC_SERVER_GRPC_PORT (set by Mythic)
ENV SPOTEXFIL_POLL_INTERVAL=30

CMD ["/spotify_server"]
