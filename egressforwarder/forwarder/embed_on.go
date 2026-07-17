//go:build withforwarder

package forwarder

import _ "embed"

// embedded is the prebuilt static linux forwarder binary (cmd/forward), baked in
// by a release/install build after `go build -o forward.bin ./egressforwarder/cmd/forward`.
// dabs carries it so a proxy box gets a single-purpose binary, never the dabs CLI.
//
//go:embed forward.bin
var embedded []byte

// EmbeddedBinary returns the embedded forwarder binary bytes.
func EmbeddedBinary() ([]byte, error) { return embedded, nil }
