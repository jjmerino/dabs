//go:build !withforwarder

package forwarder

import "errors"

// EmbeddedBinary reports that this dabs has NO forwarder embedded. A plain
// `go build ./...` takes this path so the tree always compiles, and so does
// every program importing dabs as a module — forward.bin is generated, so it is
// in neither the repo nor the module zip. Proxy egress is then unavailable and
// refuses at boot rather than misbehaving. A release/install build embeds the
// binary (see embed_on.go, tag `withforwarder`); a library caller supplies one
// instead, and the error names both doors because which one applies depends on
// who is holding the binary.
func EmbeddedBinary() ([]byte, error) {
	return nil, errors.New("no forwarder available for proxy egress: this dabs has none embedded — embedding dabs as a library, supply one with actions.Real.WithForwarder(path), any binary speaking the forwarder protocol (e.g. `go build -o forward ./egressforwarder/cmd/forward` for the box's platform); running the dabs CLI, use one built with `-tags withforwarder`")
}
