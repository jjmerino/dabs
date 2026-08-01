// Package version answers one question: which dabs is this binary?
package version

import (
	"runtime/debug"
	"strings"
)

// version is the release tag, stamped at link time by a release build
// (-X github.com/jjmerino/dabs/core/version.version=vX.Y.Z). A build from
// source leaves it empty and the line is derived from the build info instead.
var version string

// shortSHA is how many hex characters of a commit identify it in the line.
const shortSHA = 12

// Line returns the single line that identifies this binary, e.g.
// "dabs v0.6.0" or "dabs 1a2b3c4d5e6f-dirty (built from source)".
func Line() string {
	info, ok := debug.ReadBuildInfo()
	return formatLine(version, info, ok)
}

// formatLine resolves the identity line from the stamped tag and the build
// info, in that order of authority: an explicit tag wins, then a module
// version recorded by `go install`, then the commit the binary was built
// from, and failing all of those the honest answer that nothing is known.
func formatLine(stamped string, info *debug.BuildInfo, ok bool) string {
	if tag := strings.TrimSpace(stamped); tag != "" {
		return "dabs " + withV(tag)
	}
	if !ok || info == nil {
		return "dabs (unknown)"
	}
	if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
		return "dabs " + withV(v)
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision == "" {
		return "dabs (unknown)"
	}
	if len(revision) > shortSHA {
		revision = revision[:shortSHA]
	}
	if modified == "true" {
		revision += "-dirty"
	}
	return "dabs " + revision + " (built from source)"
}

// withV gives a version string exactly one leading "v", so a tag written
// either way prints the same.
func withV(v string) string {
	return "v" + strings.TrimLeft(v, "v")
}
