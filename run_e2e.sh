#!/bin/bash
# Run the e2e suite in its supported box: a privileged docker box (for the
# nested bwrap driver) carrying an overlay-enabled bubblewrap. The image the
# suite's inner boxes boot is staged into the box by the box's own Dockerfile
# (a `COPY --from` of a stage it authors), so nothing is staged on the host and
# no docker runs in the box.
#
# The suite runs in two phases, from ONE image:
#   1. hermetic — the `e2e-box` recipe carries `egress: none`, so the box has no
#      internet. The whole suite runs; the online subset self-skips (E2E_ONLINE
#      unset). No test can reach the real internet.
#   2. online — the `e2e-box-online` recipe reuses the same image with egress
#      left open, and runs ONLY the online subset with E2E_ONLINE=1. These tests
#      genuinely prove real-internet behavior (a live 200, a real redirect, a
#      real upstream cert).
set -euo pipefail

cd "$(dirname "$0")"
DABS="$(mktemp -d)/dabs"
go build -o "$DABS" .

# The set of tests that need real egress. Kept in sync with the `online(t)` calls
# in the suite; a stray addition/removal here only changes which phase runs them.
ONLINE_RE='^(TestEgressDenyList|TestProxyTerminateDomainsScopeInterception|TestEgressAllowlistDefaultDeny|TestEgressNoneCutsNetwork|TestProxyDoesNotFollowRedirects|TestProxyDirectIPThroughWindow|TestProxyOriginateForwards)$'

# The e2e box builds FROM dabs-dabseption, so that image must exist first.
#
# These two builds are also where the `build` verb is exercised: they drive real
# image builds through the driver, and `set -e` fails the run if either breaks.
# The suite cannot cover `build` itself — it runs in the box, and the box has no
# docker.
"$DABS" build dabseption
"$DABS" build test/e2e/box

# Phase 1: hermetic. The egress: none box runs the whole suite; the online subset
# self-skips inside it.
hermetic="$("$DABS" recipe test/e2e/box --detach | awk '/^id:/{print $2; exit}')"
trap '"$DABS" rm "$hermetic" --yes >/dev/null 2>&1 || true' EXIT
"$DABS" exec "$hermetic" -- go test -tags e2e -v ./test/e2e

# Phase 2: online. The same image with egress open runs ONLY the online subset.
# Selecting a named (non-default) recipe needs the box dir as the cwd registry.
trap '"$DABS" rm "$hermetic" --yes >/dev/null 2>&1 || true; "$DABS" rm "${online:-}" --yes >/dev/null 2>&1 || true' EXIT
online="$(cd test/e2e/box && "$DABS" recipe e2e-box-online --detach | awk '/^id:/{print $2; exit}')"
"$DABS" exec "$online" "E2E_ONLINE=1 go test -tags e2e -v -run '$ONLINE_RE' ./test/e2e"
