//go:build withforwarder

package forwarder

// The build the dabs CLI ships as: a forwarder is embedded. The embed keeps
// working untouched, and an explicit supply still wins over it — the precedence
// that lets a program embedding dabs choose its own forwarder whatever the build
// it links against was built with.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// CONTRACT: with nothing supplied, the embedded forwarder is materialized —
// the CLI's path, unchanged by the supply door.
func TestMaterializeFallsBackToTheEmbed(t *testing.T) {
	path, err := Materialize(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Materialize from the embed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := EmbeddedBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("materialized bytes are not the embedded forwarder")
	}
}

// CONTRACT: an explicit supply wins over the embed. A caller that names a
// forwarder gets exactly that one, so it can override even a dabs that has its
// own.
func TestSuppliedWinsOverTheEmbed(t *testing.T) {
	src := filepath.Join(t.TempDir(), "mine")
	if err := os.WriteFile(src, []byte("mine, not the embed"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := Materialize(t.TempDir(), src)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mine, not the embed" {
		t.Errorf("materialized %q, want the supplied binary", got)
	}
}
