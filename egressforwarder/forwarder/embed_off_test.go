//go:build !withforwarder

package forwarder

// The build every library consumer gets: no embedded forwarder. What matters is
// that the refusal is actionable from here, and that supplying a binary is a way
// through it rather than a second thing the embed gates.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CONTRACT: with nothing embedded and nothing supplied, proxy egress refuses,
// and the message names BOTH doors — supply a forwarder (what an embedding
// program can do) or use a CLI built with the tag (what a CLI user can do).
// Which one applies depends on who is reading, so neither may be dropped.
func TestMaterializeWithoutEmbedOrSupplyNamesBothDoors(t *testing.T) {
	path, err := Materialize(t.TempDir(), "")
	if err == nil {
		t.Fatalf("want a refusal, got %q", path)
	}
	for _, want := range []string{"WithForwarder", "-tags withforwarder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// CONTRACT: supplying a forwarder works in a build with no embed — that is the
// whole point of the door, since a module consumer never has the embed.
func TestSupplyWorksWithoutAnEmbed(t *testing.T) {
	src := filepath.Join(t.TempDir(), "forward")
	if err := os.WriteFile(src, []byte("supplied"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := Materialize(t.TempDir(), src)
	if err != nil {
		t.Fatalf("Materialize with a supplied forwarder: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "supplied" {
		t.Fatalf("materialized %q (%v), want the supplied bytes", b, err)
	}
}
