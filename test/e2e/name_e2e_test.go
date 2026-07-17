//go:build e2e

// End-to-end tests for --name (a chosen node id) and `dabs cd`. The journeys
// replicate a real working session: a repo growing a STACK of named feature
// worktrees, a dev box living on one, a box cast onto another by name, scratch
// copies for probe runs, name collisions with active work, and reusing a name
// once its holder has gone inactive. Assertions drive the same commands a user
// (or an agent) types, by NAME throughout.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNamedNodesSessionJourney is a whole session in one test:
//
//	e2e-named (project, a git repo)
//	├─ squash-fix    worktree   (dabs recipe wt --name squash-fix)
//	├─ recipes-menu  worktree   (dabs recipe wt --name recipes-menu)
//	│  └─ menu-box   box        (dabs recipe sh --worktree recipes-menu --name menu-box)
//	└─ dev-box       box        (dabs recipe wtbox --name dev-box) on its own worktree
//
// — the shape a day of feature work leaves behind. Every verb addresses nodes
// by their chosen names.
func TestNamedNodesSessionJourney(t *testing.T) {
	bundledOnly(t)
	repo := filepath.Join(home, "e2e-named")
	gitRepo(t, repo)

	// Two named feature worktrees, the way a stack of PRs is cut.
	out, code := runIn(repo, "dabs recipe wt --name squash-fix")
	if code != 0 {
		t.Fatalf("wt --name squash-fix failed (%d): %s", code, out)
	}
	// The name is the handle every message shows: the boot line carries it and
	// the branch is named after it.
	if !strings.Contains(out, "squash-fix") || !strings.Contains(out, "dabs/squash-fix") {
		t.Fatalf("boot output does not carry the chosen name and its branch:\n%s", out)
	}
	if out, code := runIn(repo, "dabs recipe wt --name recipes-menu"); code != 0 {
		t.Fatalf("wt --name recipes-menu failed (%d): %s", code, out)
	}

	// A dev box over its own fresh worktree, named — the daily driver.
	out, code = runIn(repo, "dabs recipe wtbox --name dev-box --detach")
	if code != 0 {
		t.Fatalf("wtbox --name dev-box failed (%d): %s", code, out)
	}
	if !strings.Contains(out, "id: dev-box") {
		t.Fatalf("--detach must report the chosen name as the id:\n%s", out)
	}

	// The name works everywhere an id does: exec into the box by name.
	if out, code := run("dabs exec dev-box -- cat /work/tracked.txt"); code != 0 || !strings.Contains(out, "v1") {
		t.Fatalf("exec by name failed (%d): %s", code, out)
	}

	// Cast a box ONTO an existing named worktree, naming the box too — the
	// review-a-branch move.
	out, code = run("dabs recipe sh --worktree recipes-menu --detach --name menu-box")
	if code != 0 {
		t.Fatalf("sh --worktree recipes-menu --name menu-box failed (%d): %s", code, out)
	}
	if out, code := run("dabs exec menu-box -- ls /work"); code != 0 || !strings.Contains(out, "tracked.txt") {
		t.Fatalf("box cast onto the named worktree does not see it (%d): %s", code, out)
	}

	// The tree reads as the session it was: names in the NODE column.
	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	for _, name := range []string{"squash-fix", "recipes-menu", "dev-box", "menu-box"} {
		if !strings.Contains(ls, name) {
			t.Fatalf("ls does not show node %q:\n%s", name, ls)
		}
	}
	wl, _ := run("dabs worktrees")
	if !strings.Contains(wl, "squash-fix") || !strings.Contains(wl, "dabs/recipes-menu") {
		t.Fatalf("worktrees ls does not carry the names:\n%s", wl)
	}

	// cd: the printed path IS the node's own directory — bare, absolute, one
	// uniform rule per kind — and the checkout is its held/worktree
	// subdirectory, reached with plain path arithmetic.
	out, code = run("dabs cd squash-fix")
	if code != 0 {
		t.Fatalf("cd squash-fix failed (%d): %s", code, out)
	}
	nodePath := strings.TrimSpace(out)
	if !strings.Contains(nodePath, ".dabs/nodes/squash-fix") {
		t.Fatalf("cd printed %q, want the node dir", nodePath)
	}
	if _, err := os.Stat(filepath.Join(nodePath, "held", "worktree", "tracked.txt")); err != nil {
		t.Fatalf("held/worktree under %q is not the checkout: %v", nodePath, err)
	}
	out, code = run("dabs cd dev-box")
	if code != 0 {
		t.Fatalf("cd dev-box failed (%d): %s", code, out)
	}
	if fi, err := os.Stat(strings.TrimSpace(out)); err != nil || !fi.IsDir() {
		t.Fatalf("cd dev-box printed %q, not a directory: %v", out, err)
	}
	// A prefix resolves too — same rules as every handle — but an ambiguous one
	// is refused, never guessed.
	if out, code := run("dabs cd squash"); code != 0 || strings.TrimSpace(out) != nodePath {
		t.Fatalf("cd by unambiguous prefix failed (%d): %s", code, out)
	}

	// A name held by ACTIVE work refuses a new claim — the checkout holds files.
	if out, code := runIn(repo, "dabs recipe wt --name recipes-menu"); code == 0 || !strings.Contains(out, "active") {
		t.Fatalf("claiming an active name must refuse (%d): %s", code, out)
	}

	// Wind the session down BY NAME: boxes first, then the worktrees.
	for _, cmd := range []string{
		"dabs rm menu-box -y",
		"dabs rm dev-box -y",
		"dabs rm squash-fix -y",
		"dabs rm recipes-menu -y",
	} {
		if out, code := run(cmd); code != 0 {
			t.Fatalf("%s failed (%d): %s", cmd, code, out)
		}
	}
	if left := worktreeDirs(t); len(left) != 1 { // dev-box's own worktree remains, minted id
		t.Fatalf("want only dev-box's minted worktree left, got %v", left)
	}
}

// TestNamedNodeInactiveHolderIsReapedOnTheFly: a probe run copies the cwd into
// a named node; the probe's outputs get cleaned by hand; the node lingers as an
// empty record — inactive. Reusing the name must reap that record on the fly
// and proceed, or names would be single-use.
func TestNamedNodeInactiveHolderIsReapedOnTheFly(t *testing.T) {
	bundledOnly(t)
	dir := filepath.Join(home, "e2e-probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("s1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runIn(dir, "dabs recipe scratch --name probe"); code != 0 {
		t.Fatalf("scratch --name probe failed (%d): %s", code, out)
	}
	copied := filepath.Join(nodesDir(), "probe", "held", "work")
	if _, err := os.Stat(filepath.Join(copied, "seed.txt")); err != nil {
		t.Fatalf("named scratch copy missing: %v", err)
	}

	// The probe's bytes get cleaned by hand; the node record lingers, inactive.
	entries, err := os.ReadDir(copied)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		os.RemoveAll(filepath.Join(copied, e.Name()))
	}
	inactive, _ := run("dabs ls --inactive")
	if !strings.Contains(inactive, "probe") {
		t.Fatalf("emptied probe node should be inactive:\n%s", inactive)
	}

	// Same name, next run: the empty record is reaped on the fly and reused.
	out, code := runIn(dir, "dabs recipe scratch --name probe")
	if code != 0 {
		t.Fatalf("reusing the name over an inactive holder failed (%d): %s", code, out)
	}
	if !strings.Contains(out, "inactive") {
		t.Fatalf("the on-the-fly reap must say so:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(copied, "seed.txt")); err != nil {
		t.Fatalf("fresh copy missing after name reuse: %v", err)
	}
}
