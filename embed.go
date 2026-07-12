package main

import "embed"

// imagesFS bundles dabs-owned build recipes (…) INTO the binary,
// so a recipe naming a bundled image builds it from any install.
//
//go:embed all:images
var imagesFS embed.FS

// agentsGuide is the full agent-facing guide, printed by
// `dabs --help-full-for-agents`. It is the repo's AGENTS.md, bundled so the
// complete instructions ship in any install.
//
//go:embed AGENTS.md
var agentsGuide []byte
