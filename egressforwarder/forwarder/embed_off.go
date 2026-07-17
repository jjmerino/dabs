//go:build !withforwarder

package forwarder

import "errors"

// EmbeddedBinary reports that this dabs was built WITHOUT the forwarder embedded.
// A plain `go build ./...` takes this path so the tree always compiles; proxy
// egress is then unavailable and refuses at boot rather than misbehaving. A
// release/install build embeds the binary (see embed_on.go, tag `withforwarder`).
func EmbeddedBinary() ([]byte, error) {
	return nil, errors.New("this dabs was built without an embedded forwarder — build with `-tags withforwarder` after `go build -o egressforwarder/forwarder/forward.bin ./egressforwarder/cmd/forward`")
}
