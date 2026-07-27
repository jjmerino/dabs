//go:build e2e && linux

// End-to-end proof of the LIBRARY entry point: a Go program that embeds dabs and
// boots a box from a recipe VALUE it built in memory. Unlike the rest of this
// suite — which drives the `dabs` binary on PATH — this file imports dabs and
// calls actions.Boot directly, because that is the surface under test and no CLI
// reaches it. The box and its driver are the real ones the suite's other tests
// use; only the caller differs.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/bwrap"
)

// embedded wires the actions an embedding program gets: the platform driver and
// the repo's bundled build recipes, exactly as main.go assembles them.
func embedded(t *testing.T) actions.Real {
	t.Helper()
	drv := sandbox.Lazy("bwrap", func() (sandbox.Driver, error) { return bwrap.New() })
	images := os.DirFS(filepath.Join(baseDir, "..", "..", "images"))
	return actions.New(map[string]sandbox.Driver{"local": drv}, []string{"local"}, images, data.OS{})
}

// TestLibraryBootFromInMemoryRecipe boots a box from a recipe value that exists
// only in the caller's process — no dabs.yaml on disk, no registry entry — and
// drives the box through the identity Boot returned. The chosen node name is
// what the test reaps by, so nothing outlives the run.
func TestLibraryBootFromInMemoryRecipe(t *testing.T) {
	clean(t)
	const node = "dabs-e2e-inmem"
	t.Cleanup(func() { run("dabs rm " + node + " --yes") })

	a := embedded(t)
	box, err := a.Boot(actions.BootSpec{
		Name:     sandboxName,
		NodeName: node,
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: sandboxName},
			Workdir: "/work",
			Command: []string{"sh"},
			Env:     map[string]string{"FROM_MEMORY": "yes"},
		},
	})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if box.ID != node {
		t.Fatalf("box id = %q, want the chosen name %q", box.ID, node)
	}
	if box.Instance == "" {
		t.Fatal("box instance is empty — the caller has nothing to drive")
	}
	// The returned identity is usable: both handles reach the same live box, and
	// the recipe's env — which only ever existed as a Go value — is in it.
	for _, handle := range []string{box.ID, box.Instance} {
		out, code := run("dabs exec " + handle + " -- printenv FROM_MEMORY")
		if code != 0 || !strings.Contains(out, "yes") {
			t.Fatalf("exec via %q (%d): %s", handle, code, out)
		}
	}
	// Nothing was written to disk to make this recipe: the cwd holds no dabs.yaml
	// naming it, and the registry does not know the box's name.
	if out, _ := run("dabs recipes"); strings.Contains(out, node) {
		t.Fatalf("recipes lists the in-memory recipe: %s", out)
	}
	// Detached: Boot never runs the recipe's command, and the box stays up.
	out, code := run("dabs ls")
	if code != 0 || !strings.Contains(out, node) {
		t.Fatalf("ls does not show the booted box (%d): %s", code, out)
	}
}
