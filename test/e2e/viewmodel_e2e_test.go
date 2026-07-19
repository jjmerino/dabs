//go:build e2e

// Regression tests for the ls/rm VIEW-MODEL disagreements (edition-2 hunt,
// E2-3 / E2-6 / E2-51 / E2-21). ls and rm render the same node tree through one
// view-model (core/actions/nodeview.go); these pin the places where the two
// verbs — or one verb and reality — told different stories about the same node.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nodesOfKind lists the node ids of one kind, read from the node records —
// the same way worktreeDirs finds worktrees.
func nodesOfKind(t *testing.T, kind string) []string {
	t.Helper()
	entries, err := os.ReadDir(nodesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read nodes dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(nodesDir(), e.Name(), "dabs-node.json"))
		if err != nil {
			continue
		}
		var n struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(b, &n) == nil && n.Kind == kind {
			out = append(out, e.Name())
		}
	}
	return out
}

// theProjectNode returns the single project node id, failing if there is not
// exactly one — tests here resetNodes first, so their project is the only one.
func theProjectNode(t *testing.T) string {
	t.Helper()
	projs := nodesOfKind(t, "project")
	if len(projs) != 1 {
		t.Fatalf("want exactly one project node, got %v", projs)
	}
	return projs[0]
}

// rowWith returns the first output line containing sub (and fails if none).
func rowWith(t *testing.T, out, sub string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	t.Fatalf("no row containing %q in:\n%s", sub, out)
	return ""
}

// sectionOf returns the section heading a row sits under. Headings print at
// column 0; every row under one is indented — so the nearest non-indented,
// non-empty line above the row names its section.
func sectionOf(t *testing.T, out, rowSub string) string {
	t.Helper()
	section := ""
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") {
			section = line
		}
		if strings.Contains(line, rowSub) {
			return section
		}
	}
	t.Fatalf("no row containing %q in:\n%s", rowSub, out)
	return ""
}

// E2-3: `rm` builds its cascade preview with a nil liveness map, so a box that
// is RUNNING renders as `gone` — the preview of "what I am about to destroy"
// lies about the one fact that matters most. The preview must run the same
// live query `ls` runs, and the two must agree on the box's state.
func TestRmPreviewShowsLiveBoxAsLiveE2E(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-vm-live")
	bugRecipe(t, dir, "vmlive", "")
	out, code := runIn(dir, "dabs recipe vmlive --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	boxID := nodeIDFrom(t, out)
	proj := theProjectNode(t)

	dry, code := run("dabs rm " + proj + " --dry")
	if code != 0 {
		t.Fatalf("rm --dry failed (%d): %s", code, dry)
	}
	previewRow := rowWith(t, dry, boxID)
	if strings.Contains(previewRow, "gone") {
		t.Fatalf("rm --dry shows a RUNNING box as gone (E2-3); row:\n%q\npreview:\n%s", previewRow, dry)
	}
	if !strings.Contains(previewRow, "live") {
		t.Fatalf("rm --dry does not show the running box as live (E2-3); row:\n%q\npreview:\n%s", previewRow, dry)
	}

	// Cross-check: ls and the preview agree on that node's state token.
	ls, _ := run("dabs ls")
	lsRow := rowWith(t, ls, boxID)
	if !strings.Contains(lsRow, "live") {
		t.Fatalf("ls and rm --dry disagree on the box's state (E2-3); ls row:\n%q\npreview row:\n%q", lsRow, previewRow)
	}
}

// E2-6: a project place whose only box is gone was filed under the `no place`
// heading — an error-looking bucket — although the place has a real path on THIS
// machine. Local nodes render in the flat, heading-less local tree, so the
// project must appear there and never under `no place`. The subtree is inactive
// once its box is gone and its spaces empty, so it surfaces under `ls --inactive`.
func TestLsPlaceWithDownedBoxNotUnderNoBoxE2E(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-vm-arch")
	bugRecipe(t, dir, "vmarch", "")
	out, code := runIn(dir, "dabs recipe vmarch --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	boxID := nodeIDFrom(t, out)
	proj := theProjectNode(t)

	if out, code := run("dabs rm " + boxID + " --keep -y"); code != 0 {
		t.Fatalf("rm --keep failed (%d): %s", code, out)
	}

	// The subtree is inactive now (box gone, spaces empty), so it lists under
	// `--inactive` — and there it must render in the flat local tree, never under
	// the error-looking `no place` bucket. The flat local tree is heading-less, so
	// the project's section is the empty string, not any heading.
	ls, code := run("dabs ls --inactive")
	if code != 0 {
		t.Fatalf("ls --inactive failed (%d): %s", code, ls)
	}
	rowWith(t, ls, proj) // the project renders on the machine at all
	section := sectionOf(t, ls, proj)
	if strings.HasPrefix(section, "no place") {
		t.Fatalf("a project with a real path on this machine is filed under %q (E2-6):\n%s", section, ls)
	}
	if section != "" {
		t.Fatalf("project row not in the flat local tree, got heading %q (E2-6):\n%s", section, ls)
	}
}

// E2-51: a GONE box was drawn as a PARENTLESS row under `no place` while its
// parent project sat live under `local` — two trees for one tree. A gone box must
// nest under its parent whenever the parent is shown, and `rm --dry` must show the
// same parent/child shape. A gone box only persists when it left files behind
// (an empty one is removed on down), so boxA is given a leftover volume file; the
// live boxB keeps the whole subtree active, so it shows in the default `ls`.
func TestLsNestsGoneBoxUnderLiveParentE2E(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-vm-nest")
	bugRecipe(t, dir, "vmnest", "")
	outA, code := runIn(dir, "dabs recipe vmnest --detach")
	if code != 0 {
		t.Fatalf("up A failed (%d): %s", code, outA)
	}
	boxA := nodeIDFrom(t, outA)
	outB, code := runIn(dir, "dabs recipe vmnest --detach")
	if code != 0 {
		t.Fatalf("up B failed (%d): %s", code, outB)
	}
	boxB := nodeIDFrom(t, outB)
	proj := theProjectNode(t)

	// A leftover file in boxA's volume, so its record survives being brought down.
	volA := filepath.Join(nodesDir(), boxA, "volume")
	if err := os.MkdirAll(volA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(volA, "leftover.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bring boxA down (keeping its file-holding record); boxB stays live, so the
	// subtree is active and shows by default.
	if out, code := run("dabs rm " + boxA + " --keep -y"); code != 0 {
		t.Fatalf("rm --keep failed (%d): %s", code, out)
	}

	all, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, all)
	}
	if sec := sectionOf(t, all, boxA); strings.HasPrefix(sec, "no place") {
		t.Fatalf("gone box drawn parentless under %q while its parent is shown (E2-51):\n%s", sec, all)
	}
	goneRow := rowWith(t, all, boxA)
	if !strings.Contains(goneRow, "├─") && !strings.Contains(goneRow, "└─") {
		t.Fatalf("gone box is not a child row under its parent (E2-51); row:\n%q\nls:\n%s", goneRow, all)
	}
	// It sits BENEATH its parent's row (same tree, parent first).
	if pi, bi := strings.Index(all, proj), strings.Index(all, boxA); pi < 0 || bi < pi {
		t.Fatalf("gone box row not beneath its parent row (E2-51):\n%s", all)
	}
	_ = boxB

	// Cross-check: rm --dry shows the same parent/child shape.
	dry, code := run("dabs rm " + proj + " --dry")
	if code != 0 {
		t.Fatalf("rm --dry failed (%d): %s", code, dry)
	}
	dryRow := rowWith(t, dry, boxA)
	if !strings.Contains(dryRow, "├─") && !strings.Contains(dryRow, "└─") {
		t.Fatalf("rm --dry does not nest the gone box under the parent (E2-51); row:\n%q\npreview:\n%s", dryRow, dry)
	}
}

// E2-21: ls's STATE column said `unmerged` for a worktree with ZERO commits
// ahead and only uncommitted/untracked work — but `dabs worktrees` already
// distinguishes that as has work. Only commits ahead are unmerged; local-only
// work must read as work, not as an unmerged branch.
func TestLsWorktreeStateNotUnmergedWhenZeroAheadE2E(t *testing.T) {
	clean(t)
	resetNodes(t)
	repo := filepath.Join(home, "e2e-vm-wt")
	gitRepo(t, repo)
	// An image-less worktree recipe: it cuts the worktree and runs no box.
	yaml := "default: wt\nrecipes:\n  wt:\n    sources:\n      - worktree: .\n"
	if err := os.WriteFile(filepath.Join(repo, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runIn(repo, "dabs recipe wt"); code != 0 {
		t.Fatalf("recipe wt failed (%d): %s", code, out)
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree, got %v", wts)
	}
	name := wts[0]
	wt := worktreeData(name)

	// Only uncommitted/untracked work, zero commits ahead. The checkout carries
	// files, so its subtree is active and shows in the default `ls`.
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, all)
	}
	row := rowWith(t, all, name)
	if strings.Contains(row, "unmerged") {
		t.Fatalf("worktree with 0 commits ahead reads `unmerged` (E2-21); row:\n%q\nls:\n%s", row, all)
	}

	// Commit the work: now the branch IS ahead, and unmerged is the truth.
	gitOut(t, wt, "add", "-A")
	gitOut(t, wt, "-c", "user.email=e2e@dabs.test", "-c", "user.name=e2e", "commit", "-qm", "wip")
	all2, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, all2)
	}
	row2 := rowWith(t, all2, name)
	if !strings.Contains(row2, "unmerged") {
		t.Fatalf("worktree with commits ahead does not read `unmerged` (E2-21); row:\n%q\nls:\n%s", row2, all2)
	}
}

// A SQUASH merge lands a worktree's content in the base while leaving its
// commits ahead for ever — git's commit graph never learns. The judgment is
// content, not commit count: the worktree reads no-diff in `ls` and
// `worktrees ls`, and `dabs rm` takes it WITHOUT --force — the flag is for
// discarding work, and landed work is not discarded.
func TestSquashMergedWorktreeReadsNoDiffAndReapsWithoutForce(t *testing.T) {
	clean(t)
	resetNodes(t)
	repo := filepath.Join(home, "e2e-squash-wt")
	gitRepo(t, repo)
	if out, code := runIn(repo, "dabs recipe wt"); code != 0 {
		t.Fatalf("recipe wt failed (%d): %s", code, out)
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree, got %v", wts)
	}
	name := wts[0]
	wt := worktreeData(name)

	// The agent commits work on the worktree's branch...
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wt, "add", "-A")
	gitOut(t, wt, "-c", "user.email=e2e@dabs.test", "-c", "user.name=e2e", "commit", "-qm", "feature")
	branch := strings.TrimSpace(gitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD"))

	// ...and the human lands it as a SQUASH: content in the base, commits not.
	gitOut(t, repo, "merge", "--squash", branch)
	gitOut(t, repo, "-c", "user.email=e2e@dabs.test", "-c", "user.name=e2e", "commit", "-qm", "feature (squashed)")

	// Both listings read the truth: landed, nothing unreviewed.
	all, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, all)
	}
	if row := rowWith(t, all, name); strings.Contains(row, "unmerged") {
		t.Fatalf("squash-merged worktree reads unmerged in ls; row:\n%q\nls:\n%s", row, all)
	}
	wl, code := run("dabs worktrees")
	if code != 0 {
		t.Fatalf("worktrees failed (%d): %s", code, wl)
	}
	if row := rowWith(t, wl, name); !strings.Contains(row, "no-diff") {
		t.Fatalf("squash-merged worktree not no-diff in worktrees ls; row:\n%q\n%s", row, wl)
	}

	// And the reap needs -y (the checkout holds files) but NOT --force.
	if out, code := run("dabs rm " + name + " -y"); code != 0 {
		t.Fatalf("rm of a squash-merged worktree demanded more than -y (%d): %s", code, out)
	}
	if left := worktreeDirs(t); len(left) != 0 {
		t.Fatalf("squash-merged worktree not reaped: %v", left)
	}
}
