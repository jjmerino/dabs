//go:build e2e && linux

// End-to-end proof of the LIBRARY's proxy-egress door: a program embedding dabs
// as a module has no embedded forwarder — forward.bin is generated at build
// time and ships in neither the repo nor the module zip — so it supplies one
// with actions.Real.WithForwarder. This test binary is exactly that program: it
// is built WITHOUT `-tags withforwarder`, so the embed is genuinely absent here
// and the supplied binary is the only forwarder a box can get.
//
// The forwarder supplied below is a SUPERSET: it speaks the forwarder protocol
// through the same plumbing package cmd/forward uses, and additionally drops a
// marker in the box before serving. Finding that marker is what proves the
// caller's binary — not some copy of dabs's own — is the one that ran.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/recipe"
)

// markerForwarder is a forwarder a library consumer might write: the protocol
// (`<bin> <sock> <port> -- <argv…>`, bind before exec) plus behavior of its own.
const markerForwarder = `package main

import (
	"os"
	"strconv"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

func main() {
	args := os.Args[1:]
	os.WriteFile("/out/forwarder-marker", []byte("supplied forwarder ran\n"), 0o644)
	port, err := strconv.Atoi(args[1])
	if err != nil {
		os.Exit(2)
	}
	var argv []string
	if len(args) > 2 && args[2] == "--" {
		argv = args[3:]
	}
	code, _ := forwarder.Run(args[0], port, argv)
	os.Exit(code)
}
`

// buildSuppliedForwarder builds the marker forwarder and returns its path. It
// compiles inside the dabs module, since the forwarder plumbing is a dabs
// package; the source dir is removed once the binary exists.
func buildSuppliedForwarder(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(baseDir, "..", "..")
	src := filepath.Join(repo, "e2e-supplied-forwarder")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(src) })
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(markerForwarder), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "supplied-forward")
	cmd := exec.Command("go", "build", "-o", bin, "./e2e-supplied-forwarder")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the supplied forwarder: %v: %s", err, out)
	}
	return bin
}

// proxyRecipe is a box whose egress goes through the proxy engine, with the
// caller's dir mounted at /out so the box side is observable from here. The deny
// pattern matches nothing, so the policy allows the box out while still making
// this a mapping egress — the forwarder is what is under test, not the policy.
func proxyRecipe(outDir string) recipe.Recipe {
	return recipe.Recipe{
		Image:   recipe.ImageRef{Name: sandboxName},
		Workdir: "/work",
		Command: []string{"sh"},
		Egress:  recipe.Egress{Mode: recipe.EgressProxy, Deny: []string{"denied.invalid"}},
		Sources: []recipe.Source{{Mount: outDir, Path: "/out"}},
	}
}

// CONTRACT: the forwarder the caller supplies is the one the box runs. The
// binary here is not dabs's — it is a superset with extra behavior — and it
// reaches the box unaltered, so nothing along the way compares it to the
// embedded copy or pins its version.
func TestLibrarySuppliedForwarderIsTheOneTheBoxRuns(t *testing.T) {
	clean(t)
	const node = "dabs-e2e-supplied-fwd"
	t.Cleanup(func() { run("dabs rm " + node + " --yes") })

	outDir := t.TempDir()
	if err := os.Chmod(outDir, 0o777); err != nil {
		t.Fatal(err)
	}
	bin := buildSuppliedForwarder(t)

	a := embedded(t).WithForwarder(bin)
	if _, err := a.Boot(actions.BootSpec{Name: sandboxName, NodeName: node, Recipe: proxyRecipe(outDir)}); err != nil {
		t.Fatalf("Boot with a supplied forwarder: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(outDir, "forwarder-marker"))
	if err != nil {
		t.Fatalf("the supplied forwarder left no marker in the box: %v", err)
	}
	if !strings.Contains(string(marker), "supplied forwarder ran") {
		t.Fatalf("marker = %q, want the supplied forwarder's own", marker)
	}
}

// CONTRACT: a supplied path that is not there is refused at boot, naming the
// path — a library caller's typo must not become a box booting with nothing
// mounted where its only way out belongs.
func TestLibrarySuppliedForwarderMissingIsRefused(t *testing.T) {
	clean(t)
	const node = "dabs-e2e-missing-fwd"
	t.Cleanup(func() { run("dabs rm " + node + " --yes") })

	missing := filepath.Join(t.TempDir(), "not-here")
	a := embedded(t).WithForwarder(missing)
	_, err := a.Boot(actions.BootSpec{Name: sandboxName, NodeName: node, Recipe: proxyRecipe(t.TempDir())})
	if err == nil {
		t.Fatal("a missing supplied forwarder booted a box")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not name the supplied path", err)
	}
}

// CONTRACT: without the option nothing changes — this binary has no embed, so
// proxy egress refuses exactly as it did before the door existed, and the
// refusal tells a library caller and a CLI user each what to do.
func TestLibraryWithoutTheOptionStillRefuses(t *testing.T) {
	clean(t)
	const node = "dabs-e2e-no-fwd"
	t.Cleanup(func() { run("dabs rm " + node + " --yes") })

	_, err := embedded(t).Boot(actions.BootSpec{Name: sandboxName, NodeName: node, Recipe: proxyRecipe(t.TempDir())})
	if err == nil {
		t.Fatal("proxy egress booted with no forwarder at all")
	}
	for _, want := range []string{"WithForwarder", "-tags withforwarder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
