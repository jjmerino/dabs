#!/bin/bash
# Run the e2e suite in its supported box: a privileged docker box (for the
# nested bwrap driver) carrying an overlay-enabled bubblewrap. The suite's base
# image is prebuilt on the host and staged in, so no docker runs in the box.
set -euo pipefail

cd "$(dirname "$0")"
REPO="$PWD"
DABS="$(mktemp -d)/dabs"
go build -o "$DABS" .

# Prebuild the suite's base-image rootfs on the host (mirrors bwrap.Build).
stage="$REPO/.e2e-stage"
rm -rf "$stage"; mkdir -p "$stage/rootfs"
docker build -t dabs-e2e-base test/e2e >/dev/null
cid="$(docker create dabs-e2e-base)"
docker export "$cid" | tar -x --exclude='dev/*' -C "$stage/rootfs"
docker inspect -f '{"env":{{json .Config.Env}},"workdir":{{json .Config.WorkingDir}}}' dabs-e2e-base > "$stage/image.json"
docker rm "$cid" >/dev/null; docker rmi dabs-e2e-base >/dev/null 2>&1 || true

"$DABS" build test/e2e/box
name="$("$DABS" up test/e2e/box | awk '{print $1; exit}')"
trap '"$DABS" down "$name" >/dev/null 2>&1 || true' EXIT
"$DABS" run "$name" -- go test -tags e2e -v ./test/e2e
