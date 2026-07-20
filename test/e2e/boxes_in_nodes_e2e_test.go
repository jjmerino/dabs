//go:build e2e

// End-to-end for booting a box from INSIDE a dabs worktree's own checkout
// (a cwd under ~/.dabs/nodes/<id>/held/worktree). Provisioning from under
// ~/.dabs is refused for making a project/worktree/scratch node, but booting a
// box over a worktree's checkout is the one allowed case: the box parents on
// that worktree instead of trying to mark the checkout as a new project.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nodeParent reads the parent recorded on a node.
func nodeParent(t *testing.T, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(nodesDir(), id, "dabs-node.json"))
	if err != nil {
		t.Fatalf("read node %s: %v", id, err)
	}
	var n struct {
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatalf("unmarshal node %s: %v", id, err)
	}
	return n.Parent
}

// TestBoxFromWorktreeCheckoutParentsOnWorktree drives the whole journey: cut a
// worktree with the bundled `wt` recipe, cd into its checkout, boot a bundled
// `sh` box from there, and confirm (a) it boots, (b) the box parents on the
// worktree node — no new project minted for the checkout, (c) the box's `mount:
// .` source is the checkout itself (a live bind), (d) cutting a worktree from
// inside the checkout stays refused, and (e) booting from ~/.dabs itself stays
// refused.
func TestBoxFromWorktreeCheckoutParentsOnWorktree(t *testing.T) {
	bundledOnly(t)
	repo := filepath.Join(home, "e2e-boxes-in-nodes")
	gitRepo(t, repo) // deliberately NO dabs.yaml

	// Cut a worktree (boxless bundled recipe): the checkout lands in the node's
	// held space.
	if out, code := runIn(repo, "dabs recipe wt"); code != 0 {
		t.Fatalf("bundled recipe wt failed (%d): %s", code, out)
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree node, got %v", wts)
	}
	wtName := wts[0]
	checkout := worktreeData(wtName)

	// A host-side marker in the checkout proves the box's /work is a LIVE bind of
	// this very directory, not a copy.
	if err := os.WriteFile(filepath.Join(checkout, "host-marker.txt"), []byte("live\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// (a) Boot a box from INSIDE the checkout. Without the feature this refuses as
	// "inside dabs's own storage".
	inst := bootBundled(t, checkout, "sh")

	// (b) The box parents on the WORKTREE node, and NO new project node was minted
	// for the checkout.
	boxes := nodesOfKind(t, "box")
	if len(boxes) != 1 {
		t.Fatalf("want one box node, got %v", boxes)
	}
	if p := nodeParent(t, boxes[0]); p != wtName {
		t.Fatalf("box parent = %q, want the worktree node %q", p, wtName)
	}
	// A project node minted for a cwd under ~/.dabs would be the bug — the
	// worktree already roots the chain.
	for _, proj := range nodesOfKind(t, "project") {
		dir := ""
		b, err := os.ReadFile(filepath.Join(nodesDir(), proj, "dabs-node.json"))
		if err == nil {
			var n struct {
				Dir string `json:"dir"`
			}
			if json.Unmarshal(b, &n) == nil {
				dir = n.Dir
			}
		}
		if strings.HasPrefix(dir, filepath.Join(home, ".dabs")) {
			t.Fatalf("a project node was minted for a cwd under ~/.dabs: %s (dir %s)", proj, dir)
		}
	}

	// `dabs ls` shows the box under the worktree, not as its own project.
	ls, code := run("dabs ls")
	wantExit(t, 0, code)
	wantContains(t, ls, wtName)
	wantContains(t, ls, inst)

	// (c) The `mount: .` source is the checkout itself: the host marker is visible
	// at /work and a box write reaches the host checkout (a live bind).
	out, code := run("dabs exec " + inst + " -- cat /work/host-marker.txt")
	if code != 0 || !strings.Contains(out, "live") {
		t.Fatalf("box /work is not the live checkout (%d): %s", code, out)
	}
	if out, code := run("dabs exec " + inst + " 'echo boxed > /work/from-box.txt'"); code != 0 {
		t.Fatalf("write in box failed (%d): %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(checkout, "from-box.txt")); err != nil {
		t.Fatalf("box write did not reach the checkout — /work was not a live bind: %v", err)
	}

	// (d) Cutting a worktree from inside the checkout stays refused, with the
	// specific message.
	out, code = runIn(checkout, "dabs recipe wt")
	if code == 0 {
		t.Fatalf("cutting a worktree from inside a checkout must refuse, got exit 0:\n%s", out)
	}
	wantContains(t, out, "dabs's own storage")
	wantContains(t, out, "worktree")
	if e := worktreeDirs(t); len(e) != 1 {
		t.Fatalf("a refused wt still cut a worktree; want still one, got %v", e)
	}

	// (e) Booting a box from ~/.dabs itself (not inside any node's place) stays
	// refused.
	out, code = runIn(filepath.Join(home, ".dabs"), "dabs recipe sh --detach")
	if code == 0 {
		t.Fatalf("booting from ~/.dabs itself must refuse, got exit 0:\n%s", out)
	}
	wantContains(t, out, "dabs's own storage")
}
