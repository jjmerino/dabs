#!/bin/bash
# Run the e2e suite in its supported box: a privileged docker box (for the
# nested bwrap driver) carrying an overlay-enabled bubblewrap. The image the
# suite's inner boxes boot is staged into the box by the box's own Dockerfile
# (a `COPY --from` of a stage it authors), so nothing is staged on the host and
# no docker runs in the box.
set -euo pipefail

cd "$(dirname "$0")"
DABS="$(mktemp -d)/dabs"
go build -o "$DABS" .

# The e2e box builds FROM dabs-dabseption, so that image must exist first.
#
# These two builds are also where the `build` verb is exercised: they drive real
# image builds through the driver, and `set -e` fails the run if either breaks.
# The suite cannot cover `build` itself — it runs in the box, and the box has no
# docker.
"$DABS" build dabseption
"$DABS" build test/e2e/box
name="$("$DABS" recipe test/e2e/box --detach | awk '/^id:/{print $2; exit}')"
trap '"$DABS" rm "$name" --yes >/dev/null 2>&1 || true' EXIT
"$DABS" exec "$name" -- go test -tags e2e -v ./test/e2e
