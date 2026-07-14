//go:build e2e

// Visibility follows LIFE, not history. Every boot mints a project marker for
// the directory dabs ran from, so a plain listing would fill with empty markers
// for every dir dabs was ever run in. `dabs ls` answers what is ALIVE: a subtree
// — a project and everything under it — is shown only when some node in it holds
// life (a running box, or real files in a space). `dabs ls --inactive` flips that
// to show ONLY the empty records that remain, and `dabs rm --inactive` sweeps
// them. Bringing a box down takes its node too when nothing is left, so an empty
// box never lingers as a `gone` row. These pin the rule against the real CLI.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLsHidesInactiveProjectSubtrees: a box booted then reaped with --keep leaves
// an empty project marker (the box's own record goes, since nothing was left).
// Nothing in that subtree is alive, so default `ls` must not show it; `ls
// --inactive` shows the marker that remains.
func TestLsHidesInactiveProjectSubtrees(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-inactive")
	bugRecipe(t, dir, "inact", "")
	out, code := runIn(dir, "dabs recipe inact --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	boxID := nodeIDFrom(t, out)
	proj := theProjectNode(t)

	// Reap the box but keep the record; its spaces are empty, so the box node goes
	// and only the empty project marker is left — an inactive subtree.
	if out, code := run("dabs rm " + boxID + " --keep -y"); code != 0 {
		t.Fatalf("rm --keep failed (%d): %s", code, out)
	}

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	if strings.Contains(ls, proj) || strings.Contains(ls, boxID) {
		t.Fatalf("default ls shows an inactive subtree (proj=%s box=%s):\n%s", proj, boxID, ls)
	}

	inactive, code := run("dabs ls --inactive")
	if code != 0 {
		t.Fatalf("ls --inactive failed (%d): %s", code, inactive)
	}
	wantContains(t, inactive, proj)
	// The empty box record did not linger: bringing it down took its node.
	wantNotContains(t, inactive, boxID)
}

// TestLeftoverFilesKeepASubtreeActive: a reaped box whose volume still holds a
// file left over from its life is NOT dead history — its node is kept and default
// `ls` still shows its subtree. (The volume survives `rm --keep -y`; the leftover
// file is what keeps the subtree alive.)
func TestLeftoverFilesKeepASubtreeActive(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-leftover")
	bugRecipe(t, dir, "leftover", "")
	out, code := runIn(dir, "dabs recipe leftover --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	boxID := nodeIDFrom(t, out)
	proj := theProjectNode(t)

	// A file left behind in the box node's volume space — the space `rm` keeps.
	vol := filepath.Join(nodesDir(), boxID, "volume")
	if err := os.MkdirAll(vol, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vol, "leftover.txt"), []byte("from the box\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, code := run("dabs rm " + boxID + " --keep -y"); code != 0 {
		t.Fatalf("rm --keep failed (%d): %s", code, out)
	}

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	// The leftover file keeps the subtree active: both the project and the box row
	// are shown by default.
	wantContains(t, ls, proj)
	wantContains(t, ls, boxID)
}

// TestEmptyDirTreeIsEmpty (the E2-4 pin, as activity): a space containing only
// nested EMPTY directories holds nothing. So a node whose only content is such a
// tree draws no ⚠, is reaped with no consent prompt, and counts as inactive. A
// project marker (which persists) is used so the empty-dir tree is what decides.
func TestEmptyDirTreeIsEmpty(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-emptytree")
	bugRecipe(t, dir, "emptytree", "")
	out, code := runIn(dir, "dabs recipe emptytree --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	boxID := nodeIDFrom(t, out)
	if out, code := run("dabs rm " + boxID + " -y"); code != 0 {
		t.Fatalf("rm failed (%d): %s", code, out)
	}
	proj := theProjectNode(t)

	// A tree of only empty directories in the project's held space — no file
	// anywhere.
	nested := filepath.Join(nodesDir(), proj, "held", "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// --inactive shows the marker (its subtree is inactive); its held cell has no ⚠.
	inactive, code := run("dabs ls --inactive")
	if code != 0 {
		t.Fatalf("ls --inactive failed (%d): %s", code, inactive)
	}
	row := rowWith(t, inactive, proj)
	if strings.Contains(row, "⚠") {
		t.Fatalf("a tree of only empty dirs marked as holding files (E2-4); row=%q\n%s", row, inactive)
	}

	// The subtree is inactive: default ls hides it and reports it as inactive.
	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	if strings.Contains(ls, proj) {
		t.Fatalf("empty-dir-only subtree shown by default ls:\n%s", ls)
	}
	wantContains(t, ls, "inactive")

	// rm asks nothing about the empty-dir tree: no consent prompt, no "kept" line —
	// it is reaped silently and the node removed.
	rmOut, code := run("dabs rm " + proj + " -y")
	if code != 0 {
		t.Fatalf("rm failed (%d): %s", code, rmOut)
	}
	wantContains(t, rmOut, "removed")
	if strings.Contains(rmOut, "kept") || strings.Contains(rmOut, "holds files") {
		t.Fatalf("empty-dir tree made rm ask about held files:\n%s", rmOut)
	}
}

// TestLiveBoxIsAlwaysActive: a running box is life itself — its subtree is shown
// by default `ls`, project marker and all.
func TestLiveBoxIsAlwaysActive(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-livebox")
	bugRecipe(t, dir, "livebox", "")
	out, code := runIn(dir, "dabs recipe livebox --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	boxID := nodeIDFrom(t, out)
	proj := theProjectNode(t)
	t.Cleanup(func() { run("dabs rm " + boxID + " -y") })

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	wantContains(t, ls, boxID)
	wantContains(t, ls, proj)
}

// TestFinishedBoxesAreFullyReaped: a sandbox finishing its work IS a down, and a
// down with nothing left in held/ or volume/ is a full reap — node and all. The
// non-keep recipe path must not leave a `gone` row per run: boot a few recipes to
// completion and no box node may remain, on disk or in `ls`.
func TestFinishedBoxesAreFullyReaped(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-finished")
	bugRecipe(t, dir, "finished", "")

	// Run the recipe to completion a few times. Its command is `sh`; with no
	// terminal it reads EOF and exits at once — the box came up, worked, finished.
	for i := 0; i < 3; i++ {
		if out, code := runIn(dir, "dabs recipe finished"); code != 0 {
			t.Fatalf("recipe run %d failed (%d): %s", i, code, out)
		}
	}

	// Nothing was left in any space, so no box node survives its box.
	if boxes := nodesOfKind(t, "box"); len(boxes) != 0 {
		t.Fatalf("finished boxes leaked their nodes: %v", boxes)
	}
	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	wantNotContains(t, ls, "gone")
}

// TestGoneEmptyBoxIsInactiveEvenUnderActiveParent: activity is judged per NODE,
// not inherited from the subtree. A box that is gone and holds nothing is dead
// weight even when its parent project is alive with files — default `ls` must not
// show it as an eternal `gone` row, `ls --inactive` must own it, and
// `rm --inactive` must sweep it while leaving the active parent standing.
func TestGoneEmptyBoxIsInactiveEvenUnderActiveParent(t *testing.T) {
	clean(t)
	resetNodes(t)
	dir := filepath.Join(home, "e2e-goneunder")
	bugRecipe(t, dir, "goneunder", "")
	out, code := runIn(dir, "dabs recipe goneunder --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	liveBox := nodeIDFrom(t, out)
	t.Cleanup(func() { run("dabs rm " + liveBox + " -y") })
	proj := theProjectNode(t)

	// The parent holds files, so its subtree is active.
	vol := filepath.Join(nodesDir(), proj, "volume")
	if err := os.MkdirAll(vol, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vol, "data.txt"), []byte("alive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A gone, all-empty box under that parent — what a crashed or killed box
	// leaves behind: a record whose instance the driver no longer knows.
	goneID := "goneunder-deadbox"
	node := filepath.Join(nodesDir(), goneID)
	for _, sp := range []string{"volume", "held", "tmp"} {
		if err := os.MkdirAll(filepath.Join(node, sp), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := `{"id":"` + goneID + `","kind":"box","parent":"` + proj + `","recipe":"goneunder","created":"2020-01-01T00:00:00Z","instance":"goneunder-nosuchbox"}`
	if err := os.WriteFile(filepath.Join(node, "dabs-node.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(node) })

	// Default ls shows the living (parent, live box) and not the dead row.
	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	wantContains(t, ls, proj)
	wantNotContains(t, ls, goneID)

	// The dead box is the --inactive view's to show…
	inactive, code := run("dabs ls --inactive")
	if code != 0 {
		t.Fatalf("ls --inactive failed (%d): %s", code, inactive)
	}
	wantContains(t, inactive, goneID)

	// …and the --inactive sweep's to reap: its node goes, the active parent stays.
	if out, code := run("dabs rm --inactive"); code != 0 {
		t.Fatalf("rm --inactive failed (%d): %s", code, out)
	}
	if _, err := os.Stat(node); !os.IsNotExist(err) {
		t.Fatalf("gone empty box node survived rm --inactive (stat err=%v)", err)
	}
	ls, code = run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	wantContains(t, ls, proj)
	wantContains(t, ls, liveBox)
}

// TestRmInactiveSweepsOnlyInactive: `dabs rm --inactive` reaps every inactive
// subtree (the empty markers `ls` hides) and leaves active ones standing.
func TestRmInactiveSweepsOnlyInactive(t *testing.T) {
	clean(t)
	resetNodes(t)

	// An inactive subtree: boot a box then reap it with --keep so its record goes
	// and only the empty project marker remains.
	deadDir := filepath.Join(home, "e2e-sweep-dead")
	bugRecipe(t, deadDir, "sweepdead", "")
	out, code := runIn(deadDir, "dabs recipe sweepdead --detach")
	if code != 0 {
		t.Fatalf("dead up failed (%d): %s", code, out)
	}
	deadBox := nodeIDFrom(t, out)
	if out, code := run("dabs rm " + deadBox + " --keep -y"); code != 0 {
		t.Fatalf("rm --keep failed (%d): %s", code, out)
	}

	// An active subtree: a live box.
	liveDir := filepath.Join(home, "e2e-sweep-live")
	bugRecipe(t, liveDir, "sweeplive", "")
	out, code = runIn(liveDir, "dabs recipe sweeplive --detach")
	if code != 0 {
		t.Fatalf("live up failed (%d): %s", code, out)
	}
	liveBox := nodeIDFrom(t, out)
	t.Cleanup(func() { run("dabs rm " + liveBox + " -y") })

	// Before the sweep, there IS an inactive subtree to see.
	before, _ := run("dabs ls --inactive")
	if strings.Contains(before, "no inactive subtrees") {
		t.Fatalf("expected an inactive subtree before the sweep:\n%s", before)
	}

	if out, code := run("dabs rm --inactive"); code != 0 {
		t.Fatalf("rm --inactive failed (%d): %s", code, out)
	}

	// The inactive marker is gone; the live box still stands.
	after, code := run("dabs ls --inactive")
	if code != 0 {
		t.Fatalf("ls --inactive failed (%d): %s", code, after)
	}
	wantContains(t, after, "no inactive subtrees")

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	wantContains(t, ls, liveBox)
}
