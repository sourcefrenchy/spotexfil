#!/bin/sh
# Stage the spotexfil Go agent module (<repo>/go) into ./agent_code so the
# Dockerfile can COPY it into the image (the docker build context cannot
# reach outside this folder). Re-run after any change to the Go source.
set -e
SRC="$(cd "$(dirname "$0")/../../../go" && pwd)"
DST="$(cd "$(dirname "$0")" && pwd)/agent_code"
echo "Staging $SRC -> $DST"
rm -rf "$DST"
mkdir -p "$DST"
rsync -a --exclude '.git' "$SRC"/ "$DST"/
echo "Done. Build the container with mythic-cli (or docker build .)."
