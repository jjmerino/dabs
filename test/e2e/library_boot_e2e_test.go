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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/bwrap"
)

// embedded wires the actions an embedding program gets. It is the shape of
// main.go's wiring — one lazy local driver, an images FS, the OS data layer —
// with this box's platform driver and the repo's images dir named directly,
// since neither the build-tagged selection nor the embed lives in this package.
func embedded(t *testing.T) actions.Real {
	t.Helper()
	drv := sandbox.Lazy("bwrap", func() (sandbox.Driver, error) { return bwrap.New() })
	images := os.DirFS(filepath.Join(baseDir, "..", "..", "images"))
	// A library consumer's executable is this test binary, which serves no
	// `services relay` — the box relays come from a real dabs, exactly as an
	// embedding program must arrange.
	dabsBin, err := exec.LookPath("dabs")
	if err != nil {
		t.Fatalf("no dabs on PATH for the box relays: %v", err)
	}
	return actions.New(map[string]sandbox.Driver{"local": drv}, []string{"local"}, images, data.OS{}).WithRelayExecutable(dabsBin)
}

// TestLibraryBootFromInMemoryRecipe boots a box from a recipe value that exists
// only in the caller's process — no dabs.yaml on disk, no registry entry — and
// drives the box through the identity Boot returned. The chosen node name is
// what the test reaps by, so nothing outlives the run.
func TestLibraryBootFromInMemoryRecipe(t *testing.T) {
	clean(t)
	const node = "dabs-e2e-inmem"
	// A string that exists nowhere but in the value below, so finding it anywhere
	// on disk means something serialized the recipe.
	const marker = "FROM_MEMORY"
	t.Cleanup(func() { run("dabs rm " + node + " --yes") })

	a := embedded(t)
	box, err := a.Boot(actions.BootSpec{
		Name:     sandboxName,
		NodeName: node,
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: sandboxName},
			Workdir: "/work",
			Command: []string{"sh"},
			Env:     map[string]string{marker: "yes"},
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
		out, code := run("dabs exec " + handle + " -- printenv " + marker)
		if code != 0 || !strings.Contains(out, "yes") {
			t.Fatalf("exec via %q (%d): %s", handle, code, out)
		}
	}
	// Nothing was serialized to make this recipe: no dabs.yaml appeared beside the
	// caller, and the merged registry — every layer of it — carries no trace of the
	// recipe's own marker.
	for _, f := range []string{filepath.Join(baseDir, "dabs.yaml"), filepath.Join(home, ".dabs", "recipes.yaml")} {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), marker) {
			t.Fatalf("the boot wrote the recipe into %s", f)
		}
	}
	// `recipes --print` dumps the whole MERGED registry — bundled, ~/.dabs, and
	// the cwd's dabs.yaml — so the marker's absence covers every layer.
	if out, _ := run("dabs recipes --print"); strings.Contains(out, marker) {
		t.Fatalf("the in-memory recipe reached the registry: %s", out)
	}
	// The box outlives the call: Boot tears nothing down, so `ls` still lists it.
	out, code := run("dabs ls")
	if code != 0 || !strings.Contains(out, node) {
		t.Fatalf("ls does not show the booted box (%d): %s", code, out)
	}
}
