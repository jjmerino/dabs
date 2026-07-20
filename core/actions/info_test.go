package actions_test

// Tests for `dabs info <node>`: it renders one node's full model — kind and id,
// location, the three spaces, and the recipe that provisioned it. The recipe is
// read from the SNAPSHOT persisted on the node at creation, so it is truthful
// even when the current registry no longer defines that name.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// CONTRACT: a node carrying a recipe SNAPSHOT renders that snapshot — image,
// command, and mounts — INDEPENDENT of the current registry. The registry here
// does NOT define "claude", proving info reads the persisted spec, not a fresh
// lookup. The node's location and three spaces render too, with a held space
// reported as holding files.
func TestInfoRendersPersistedRecipeSnapshot(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "boxy-abcd/dabs-node.json": []byte(`{"id":"boxy-abcd","kind":"box","instance":"inst-b","recipe":"claude","created":"t",` +
			`"recipeSpec":{"image":"claude-img","command":["claude","--dangerously"],` +
			`"env":{"CLAUDE_CONFIG_DIR":"/root/.claude"},` +
			`"sources":[{"mkmount":"~/.dabs/shared/claude","path":"/root/.claude"},{"mount":".","path":"/work"}]}}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"boxy-abcd"}}
	spaceHeld(fd, "boxy-abcd", "held") // a held space that holds files

	out := captureStdout(t, func() {
		// Empty user recipes.yaml: the bundled registry has no "claude" recipe,
		// so a green test proves the snapshot — not the registry — was rendered.
		if err := newReal("", fd, &fakeDriver{}).Info(params.Info{Node: "boxy-abcd"}); err != nil {
			t.Fatalf("Info: %v", err)
		}
	})

	for _, want := range []string{
		"boxy-abcd", "box", // kind and id
		"inst-b",                // the box instance
		".dabs/nodes/boxy-abcd", // location (a box falls back to its node dir)
		"VOL", "HELD", "TMP",    // the three spaces
		"holds files",                     // the held space's status
		"claude",                          // the recipe name
		"claude-img",                      // the snapshot's image
		"claude --dangerously",            // the snapshot's command
		"CLAUDE_CONFIG_DIR=/root/.claude", // the snapshot's env
		"~/.dabs/shared/claude",           // a snapshot source origin
		"/root/.claude",                   // its box path
		"/work",                           // the mount's box path
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
	// TEETH: the command must come from the SNAPSHOT. If info regressed to a
	// registry lookup, the unknown "claude" recipe would render nothing here.
	if !strings.Contains(out, "claude --dangerously") {
		t.Fatalf("the snapshot's command did not render — info fell back to the registry:\n%s", out)
	}
}

// CONTRACT: a node with a recipe NAME but no snapshot, whose name the registry
// no longer defines, does not error — info says the spec is unavailable.
func TestInfoMissingSnapshotUnknownRecipeIsGraceful(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "old-abcd/dabs-node.json": []byte(`{"id":"old-abcd","kind":"box","instance":"inst-o","recipe":"gone-recipe","created":"t"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"old-abcd"}}

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Info(params.Info{Node: "old-abcd"}); err != nil {
			t.Fatalf("Info must not error on a missing snapshot: %v", err)
		}
	})
	if !strings.Contains(out, "old-abcd") {
		t.Fatalf("the node itself must still render:\n%s", out)
	}
	if !strings.Contains(out, "gone-recipe") || !strings.Contains(out, "no snapshot") {
		t.Fatalf("info must name the recipe and say the snapshot is unavailable:\n%s", out)
	}
}

// CONTRACT: a boxless recipe that provisions a WORKTREE node persists the
// resolved recipe snapshot on that node too — not just on box nodes. `dabs info`
// on the worktree then renders the creation-time snapshot (labelled so), never
// the registry fallback. This drives the real provisioning path end to end.
func TestInfoWorktreeNodeRendersProvisionedSnapshot(t *testing.T) {
	y := `recipes:
  cut:
    description: cut a worktree, no box
    command: [echo, hi]
    env:
      FOO: bar
    sources:
      - worktree: .
`
	fd := baseData()
	fd.toplevel["/cwd"] = nil // the cwd is a git repo with commits
	r := newRealNoDriver(y, fd)
	captureStdout(t, func() {
		if err := r.Recipe(params.Recipe{Name: "cut"}); err != nil {
			t.Fatalf("boxless worktree recipe: %v", err)
		}
	})

	// Find the worktree node the recipe cut, and assert the snapshot landed on the
	// persisted record itself — proof the provisioning path wrote it.
	var wtID string
	for p, b := range fd.files {
		if !strings.HasSuffix(p, "/dabs-node.json") {
			continue
		}
		var n map[string]any
		if err := json.Unmarshal(b, &n); err != nil {
			continue
		}
		if n["kind"] == "worktree" && n["worktree"] != nil {
			wtID, _ = n["id"].(string)
			if n["recipeSpec"] == nil {
				t.Fatalf("worktree node persisted no recipeSpec:\n%s", b)
			}
		}
	}
	if wtID == "" {
		t.Fatal("the boxless recipe cut no worktree node")
	}

	out := captureStdout(t, func() {
		if err := r.Info(params.Info{Node: wtID}); err != nil {
			t.Fatalf("Info: %v", err)
		}
	})
	// The note proves the SNAPSHOT was rendered — the registry (which still holds
	// "cut") would otherwise supply the fallback and read "current registry
	// definition". If the snapshot regressed, this label would flip.
	if !strings.Contains(out, "snapshot at creation") {
		t.Fatalf("info must render the persisted snapshot, not the registry fallback:\n%s", out)
	}
	for _, want := range []string{"cut", "echo hi", "FOO=bar"} {
		if !strings.Contains(out, want) {
			t.Errorf("info missing snapshot detail %q:\n%s", want, out)
		}
	}
	// The lone `worktree: .` source has no in-box path, so its mounts row is the
	// kind and origin alone — no arrow, no empty target.
	if !strings.Contains(out, "worktree  .") {
		t.Errorf("boxless worktree source must render `worktree  .`:\n%s", out)
	}
	if strings.Contains(out, "→") {
		t.Errorf("a pathless source must not render an arrow:\n%s", out)
	}
}

// CONTRACT: an unknown node is a clean error naming the miss, not a panic.
func TestInfoUnknownNodeErrors(t *testing.T) {
	fd := baseData()
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {}}
	err := newReal("", fd, &fakeDriver{}).Info(params.Info{Node: "nope"})
	if err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("want a 'no node' error, got %v", err)
	}
}
