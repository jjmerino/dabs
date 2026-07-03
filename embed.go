package main

import "embed"

// harnessFS bundles the harness integrations INTO the dabs binary, so
// `dabs install` works from anywhere — a downloaded release, $GOBIN, wherever
// — not only from a source checkout.
//
//go:embed all:harnesses
var harnessFS embed.FS
