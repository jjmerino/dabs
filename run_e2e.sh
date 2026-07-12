#!/bin/bash
# Run the e2e suite in its supported box: a privileged docker box (for the
# nested bwrap driver) carrying an overlay-enabled bubblewrap. The suite's base
# image is staged into the box by the box's Dockerfile, so no docker runs in the
# box.
set -euo pipefail

cd "$(dirname "$0")"
DABS="$(mktemp -d)/dabs"
go build -o "$DABS" .

# Build the inner base image. test/e2e/box's Dockerfile flattens it into a dabs
# bwrap image with `COPY --from=dabs-e2e-base` — the builder does the export, so
# nothing here has to reimplement bwrap.Build in shell.
docker build -t dabs-e2e-base test/e2e >/dev/null

"$DABS" build test/e2e/box
name="$("$DABS" up test/e2e/box | awk '{print $1; exit}')"
trap '"$DABS" down "$name" >/dev/null 2>&1 || true' EXIT
"$DABS" run "$name" -- go test -tags e2e -v ./test/e2e
