# Mythic C2 profile container for the "spotify" profile.
#
# The container needs a copy of the spotexfil Go module (the transport
# library lives in this repo's go/ directory). Build from the REPO ROOT:
#
#   docker build -f mythic/C2_Profiles/spotify/Dockerfile -t spotify-c2 .
#
# The go.mod `replace` directive points at ../../../go for local development;
# it is rewritten below (via sed) to the in-context copy.

FROM golang:1.23 AS builder

WORKDIR /build

# Copy the spotexfil transport module and the profile container source.
COPY go/ /spotexfil/
COPY mythic/C2_Profiles/spotify/ /build/

# Point the replace directive at the in-context copy of spotexfil.
RUN sed -i '' -e 's#=> ../../../go#=> /spotexfil#' go.mod 2>/dev/null || \
    sed -i -e 's#=> ../../../go#=> /spotexfil#' go.mod

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
