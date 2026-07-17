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
