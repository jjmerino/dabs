package main

import "embed"

// imagesFS bundles dabs-owned build recipes (the auth box, …) INTO the binary,
// so commands like `dabs auth claude` build their image from any install.
//
//go:embed all:images
var imagesFS embed.FS

// agentsGuide is the full agent-facing guide, printed by
// `dabs --help-full-for-agents`. It is the repo's AGENTS.md, bundled so the
// complete instructions ship in any install.
//
//go:embed AGENTS.md
var agentsGuide []byte
