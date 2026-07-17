#!/bin/bash
# Rebuild and install dabs from this checkout into $GOBIN (~/go/bin).
set -euo pipefail

cd "$(dirname "$0")/.."
# Build the single-purpose forwarder as a static LINUX binary (boxes are linux,
# whatever the host is), then install dabs with it embedded (-tags withforwarder)
# so proxy egress can materialize and mount it. Without the tag dabs still builds,
# but proxy egress refuses at boot.
GOOS=linux GOARCH="$(go env GOARCH)" CGO_ENABLED=0 \
  go build -o egressforwarder/forwarder/forward.bin ./egressforwarder/cmd/forward
go install -tags withforwarder .
echo "installed: $(ls -l "$(go env GOPATH)/bin/dabs")"

# The proxy egress engine runs on bun (host-side) and mints leaf certs with
# openssl. Ensure both are on PATH so `egress: {proxy: ...}` works. Without them
# dabs still builds and open/none egress are unaffected — only proxy egress needs
# them, and it fails clearly at boot if they are missing.
if ! command -v bun >/dev/null 2>&1; then
  echo "bun not found — installing it (the proxy engine's runtime)…"
  curl -fsSL https://bun.sh/install | bash
  echo "note: add \"\$HOME/.bun/bin\" to your PATH (the installer updates your shell profile — open a new shell or source it), so dabs can spawn bun."
fi
command -v openssl >/dev/null 2>&1 \
  || echo "warning: openssl not found — proxy egress with 'tls: terminate' needs it to mint certs; install it before using a terminate window."
