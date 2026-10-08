#!/usr/bin/env bash
# stage.sh - copy the spotexfil Go module into this directory so the
# mythic-cli docker build (which uses this directory as its context)
# can reach it. Re-run after changing anything under <repo>/go.
set -euo pipefail

SRC="$(cd "$(dirname "$0")/../../../go" && pwd)"
DST="$(cd "$(dirname "$0")" && pwd)/spotexfil_src"

echo "Staging $SRC -> $DST"
rm -rf "$DST"
mkdir -p "$DST"
rsync -a --delete \
  --exclude '.git' \
  --exclude 'cmd/spotexfil/dist' \
  "$SRC/" "$DST/"
echo "Done."
