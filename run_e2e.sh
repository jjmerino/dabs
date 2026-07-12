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

# The e2e box DERIVES from the dabseption box (FROM dabs-dabseption) rather than
# reimplementing it, so build that first — it is the one definition of "a box that
# can run dabs and boot its own boxes".
#
# These two builds are also the `dabs build` verb's exercise: they drive real
# image builds through the driver, and `set -e` fails the run if either breaks.
# The suite itself cannot cover `build` — it runs in the box, and the box has no
# docker.
"$DABS" build dabseption
"$DABS" build test/e2e/box
name="$("$DABS" up test/e2e/box | awk '{print $1; exit}')"
trap '"$DABS" down "$name" >/dev/null 2>&1 || true' EXIT
"$DABS" run "$name" -- go test -tags e2e -v ./test/e2e
