#!/bin/bash
# Rebuild and install dabs from this checkout into $GOBIN (~/go/bin).
set -euo pipefail

cd "$(dirname "$0")/.."
go install .
echo "installed: $(ls -l "$(go env GOPATH)/bin/dabs")"
