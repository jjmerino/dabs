// Package clidriver holds the logic the CLI-backed drivers (apple, docker)
// share verbatim: minting an instance name, rendering a bind mount, and
// filtering a vendor image listing down to dabs's own. It is build-tag-free so
// both a darwin-only and an all-platform driver may import it. Genuine
// per-driver differences (the CLI name, JSON-vs-format listing, TTY handling)
// stay in the driver.
package clidriver

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jjmerino/dabs/core/sandbox"
)

// Prefix namespaces every image and instance dabs manages.
const Prefix = "dabs-"

// InstanceName mints <specName>-<id> with random hex — unguessable, and
// addressable by unique prefix. The error carries no driver prefix; the caller
// wraps it with its own.
func InstanceName(specName string) (string, error) {
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("generating instance id: %w", err)
	}
	return fmt.Sprintf("%s-%s", specName, hex.EncodeToString(id)), nil
}

// MountArg renders one live host mount as a `--mount` value for the container
// CLIs: a bind whose writes pass through to the host, read-only when asked.
func MountArg(m sandbox.Mount) string {
	arg := fmt.Sprintf("type=bind,source=%s,target=%s", m.Host, m.Path)
	if m.RO {
		arg += ",readonly"
	}
	return arg
}

// FilterPrefixed keeps the names carrying dabs's Prefix, strips it, and
// deduplicates — the shared tail of every CLI driver's image listing.
func FilterPrefixed(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !strings.HasPrefix(n, Prefix) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, strings.TrimPrefix(n, Prefix))
	}
	return out
}
