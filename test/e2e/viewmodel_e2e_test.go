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

// E2-6: a live project place whose only box is archived was filed under the
// `no place` heading — an error-looking bucket — although the place has a real
// path on THIS machine. It belongs under the machine's own section.
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

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	section := sectionOf(t, ls, proj)
	if strings.HasPrefix(section, "no place") {
		t.Fatalf("a project with a real path on this machine is filed under %q (E2-6):\n%s", section, ls)
	}
	if !strings.HasPrefix(section, "local") {
		t.Fatalf("project row not under the machine's section, got %q (E2-6):\n%s", section, ls)
	}
}

// E2-51: `ls --all` drew an archived box as a PARENTLESS row under `no place`
// while its parent project sat live under `local` — two trees for one tree.
// The archived box must nest under its parent whenever the parent is shown,
// and `rm --dry` must show the same parent/child shape.
func TestLsAllNestsArchivedBoxUnderLiveParentE2E(t *testing.T) {
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

	// Archive one box; the other stays live, so the project is shown under local.
	if out, code := run("dabs rm " + boxA + " --keep -y"); code != 0 {
		t.Fatalf("rm --keep failed (%d): %s", code, out)
	}

	all, code := run("dabs ls --all")
	if code != 0 {
		t.Fatalf("ls --all failed (%d): %s", code, all)
	}
	if sec := sectionOf(t, all, boxA); strings.HasPrefix(sec, "no place") {
		t.Fatalf("archived box drawn parentless under %q while its parent is shown (E2-51):\n%s", sec, all)
	}
	archRow := rowWith(t, all, boxA)
	if !strings.Contains(archRow, "├─") && !strings.Contains(archRow, "└─") {
		t.Fatalf("archived box is not a child row under its parent (E2-51); row:\n%q\nls --all:\n%s", archRow, all)
	}
	// It sits BENEATH its parent's row (same tree, parent first).
	if pi, bi := strings.Index(all, proj), strings.Index(all, boxA); pi < 0 || bi < pi {
		t.Fatalf("archived box row not beneath its parent row (E2-51):\n%s", all)
	}
	_ = boxB

	// Cross-check: rm --dry shows the same parent/child shape.
	dry, code := run("dabs rm " + proj + " --dry")
	if code != 0 {
		t.Fatalf("rm --dry failed (%d): %s", code, dry)
	}
	dryRow := rowWith(t, dry, boxA)
	if !strings.Contains(dryRow, "├─") && !strings.Contains(dryRow, "└─") {
		t.Fatalf("rm --dry does not nest the archived box under the parent (E2-51); row:\n%q\npreview:\n%s", dryRow, dry)
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

	// Only uncommitted/untracked work, zero commits ahead.
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, code := run("dabs ls --all")
	if code != 0 {
		t.Fatalf("ls --all failed (%d): %s", code, all)
	}
	row := rowWith(t, all, name)
	if strings.Contains(row, "unmerged") {
		t.Fatalf("worktree with 0 commits ahead reads `unmerged` (E2-21); row:\n%q\nls:\n%s", row, all)
	}

	// Commit the work: now the branch IS ahead, and unmerged is the truth.
	gitOut(t, wt, "add", "-A")
	gitOut(t, wt, "-c", "user.email=e2e@dabs.test", "-c", "user.name=e2e", "commit", "-qm", "wip")
	all2, code := run("dabs ls --all")
	if code != 0 {
		t.Fatalf("ls --all failed (%d): %s", code, all2)
	}
	row2 := rowWith(t, all2, name)
	if !strings.Contains(row2, "unmerged") {
		t.Fatalf("worktree with commits ahead does not read `unmerged` (E2-21); row:\n%q\nls:\n%s", row2, all2)
	}
}
