package actions_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// CONTRACT: `ls` marks a worktree holding work with a `*`, and says so in the
// heading. A tree of places with no box must not read as a tree of things that do
// not matter — one of them may be an agent's afternoon, and `down`/`rm` would
// take it. The mark asks the same question `dabs worktrees` answers with HAS WORK.
func TestLsStarsWorktreesHoldingWork(t *testing.T) {
	const dirtyID, cleanID = "wt-dirty01", "wt-clean1"
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{}
	fd.dirs = map[string][]string{}
	for _, id := range []string{dirtyID, cleanID} {
		fd.files[base+id+"/dabs-node.json"] = []byte(
			`{"id":"` + id + `","kind":"worktree","worktree":{"branch":"dabs/` + id + `","repo":"/repo"}}`)
	}
	fd.dirs["/home/t/.dabs/nodes"] = []string{dirtyID, cleanID}
	// A real worktree checkout carries files, so its held space holds them — that
	// is what makes the subtree ACTIVE and shows it in the default `ls`.
	spaceHeld(fd, dirtyID, "held")
	spaceHeld(fd, cleanID, "held")
	// Only the checkout that exists is read; both resolve to held/worktree.
	fd.states[base+dirtyID+"/held/worktree"] = wtState{branch: "dabs/" + dirtyID, dirty: true}
	fd.states[base+cleanID+"/held/worktree"] = wtState{branch: "dabs/" + cleanID}

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "(location)") {
			continue // the muted location pseudo-row carries a git signal, not the STATE judgment
		}
		switch {
		case strings.Contains(line, dirtyID) && !strings.Contains(line, "has work"):
			t.Errorf("worktree holding uncommitted work is not marked `has work`:\n%s", line)
		case strings.Contains(line, cleanID) && (strings.Contains(line, "has work") || strings.Contains(line, "unmerged")):
			t.Errorf("clean worktree is marked as holding work; the mark would mean nothing:\n%s", line)
		}
	}
	if !strings.Contains(out, "has work") {
		t.Errorf("the heading does not say what the * means:\n%s", out)
	}
}

// CONTRACT: for each project node whose dir is a git repo, `ls` also lists the
// repo's worktrees dabs does NOT own — cut by git directly or another tool.
// Their NODE cell reads `(unmanaged)`, KIND worktree, WHERE the checkout's
// path, STATE the same git judgment dabs's own rows get; the space cells are
// empty. A worktree matching one of dabs's own worktree nodes is not repeated.
func TestLsRendersForeignWorktrees(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj1/dabs-node.json": []byte(`{"id":"proj1","kind":"project","dir":"/repo","created":"1"}`),
		base + "wt-owned1/dabs-node.json": []byte(
			`{"id":"wt-owned1","kind":"worktree","parent":"proj1","created":"2","worktree":{"branch":"dabs/wt-owned1","repo":"/repo"}}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj1", "wt-owned1"}}
	spaceHeld(fd, "proj1", "held") // real files: the subtree is ACTIVE, so ls shows it
	ownedPath := base + "wt-owned1/held/worktree"
	fd.foreign = map[string][]string{"/repo": {ownedPath, "/elsewhere/foreign-wt"}}
	fd.states["/elsewhere/foreign-wt"] = wtState{branch: "feature", dirty: true}

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	if got := strings.Count(out, "(unmanaged)"); got != 1 {
		t.Fatalf("want exactly one (unmanaged) row (the owned worktree must not repeat), got %d:\n%s", got, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "(unmanaged)") {
			continue
		}
		if !strings.Contains(line, "worktree") {
			t.Errorf("the row's KIND is not worktree:\n%s", line)
		}
		if !strings.Contains(line, "/elsewhere/foreign-wt") {
			t.Errorf("the row's WHERE is not the checkout's path:\n%s", line)
		}
		if !strings.Contains(line, "has work") {
			t.Errorf("a dirty foreign worktree must carry the same STATE judgment:\n%s", line)
		}
	}
}

// CONTRACT: with no git to answer (or a project dir that is not a repo),
// foreign worktrees simply do not render — no row, no warning, no error.
func TestLsForeignWorktreesDegradeWithoutGit(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj1/dabs-node.json": []byte(`{"id":"proj1","kind":"project","dir":"/repo","created":"1"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj1"}}
	spaceHeld(fd, "proj1", "held")
	// fd.foreign is nil: every GitListWorktrees call errors, like a machine
	// with no git binary.

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls must not fail when git cannot answer: %v", err)
		}
	})
	if strings.Contains(out, "(unmanaged)") {
		t.Fatalf("no git, no rows:\n%s", out)
	}
	if !strings.Contains(out, "proj1") {
		t.Fatalf("the project itself must still render:\n%s", out)
	}
}

// CONTRACT: a project with no live box and empty spaces is ACTIVE when its
// repo holds an unmerged externally-managed worktree — the unmerged checkout
// IS life. It renders in the default `ls` with its `(unmanaged)` row, the
// inactive counter does not count it, and `rm --inactive` spares it.
func TestUnmergedForeignWorktreeMakesProjectActive(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj1/dabs-node.json": []byte(`{"id":"proj1","kind":"project","dir":"/repo","created":"1"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj1"}}
	fd.foreign = map[string][]string{"/repo": {"/elsewhere/foreign-wt"}}
	fd.states["/elsewhere/foreign-wt"] = wtState{branch: "feature", dirty: true}
	r := newReal("", fd, &fakeDriver{})

	out := captureStdout(t, func() {
		if err := r.Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})
	if !strings.Contains(out, "proj1") || !strings.Contains(out, "(unmanaged)") {
		t.Fatalf("the project must render in the default ls with its row:\n%s", out)
	}
	if strings.Contains(out, "inactive") {
		t.Fatalf("an active project must not be counted inactive:\n%s", out)
	}

	// The same activity spares it from the inactive sweep.
	out = captureStdout(t, func() {
		if err := r.Rm(params.Rm{Inactive: true}); err != nil {
			t.Fatalf("rm --inactive: %v", err)
		}
	})
	if !strings.Contains(out, "no inactive subtrees") {
		t.Fatalf("rm --inactive must find nothing to sweep:\n%s", out)
	}
	if _, ok := fd.files[base+"proj1/dabs-node.json"]; !ok {
		t.Fatalf("rm --inactive reaped an active project")
	}
}

// CONTRACT: a clean, fully-merged foreign worktree is finished work — no row,
// and it does not pull an inactive project into the default listing.
func TestLsMergedCleanForeignWorktreeNoRow(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj1/dabs-node.json": []byte(`{"id":"proj1","kind":"project","dir":"/repo","created":"1"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj1"}}
	fd.foreign = map[string][]string{"/repo": {"/elsewhere/merged-wt"}}
	fd.states["/elsewhere/merged-wt"] = wtState{branch: "done"} // clean, nothing ahead

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})
	if strings.Contains(out, "(unmanaged)") {
		t.Fatalf("a merged, clean foreign worktree must not render:\n%s", out)
	}
	if strings.Contains(out, "proj1") {
		t.Fatalf("an inactive project with only finished foreign work must stay out of the default listing:\n%s", out)
	}
}

// CONTRACT: two project nodes over the SAME repository — one on the main
// checkout, one whose dir is itself a DIRTY linked worktree — enumerate the
// shared git registry ONCE, under the main-checkout project (even when the
// other node is older). Every unmerged linked worktree renders exactly once
// there, INCLUDING the one the other project stands on: another project node
// on a checkout does not suppress its row, it just isn't the enumerator.
func TestLsForeignWorktreesDedupeAcrossProjectsOfOneRepo(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "projmain/dabs-node.json": []byte(`{"id":"projmain","kind":"project","dir":"/repo","created":"2"}`),
		base + "projwt/dabs-node.json":   []byte(`{"id":"projwt","kind":"project","dir":"/repo-wt","created":"1"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"projmain", "projwt"}}
	spaceHeld(fd, "projmain", "held")
	spaceHeld(fd, "projwt", "held")
	// Both dirs answer with the same repository identity …
	fd.commondir = map[string]string{"/repo": "/repo/.git", "/repo-wt": "/repo/.git"}
	// … and the same registry: the linked worktree projwt stands on, plus one
	// no-project foreign worktree — both dirty, both must render.
	shared := []string{"/repo-wt", "/elsewhere/foreign-wt"}
	fd.foreign = map[string][]string{"/repo": shared, "/repo-wt": shared}
	fd.states["/repo-wt"] = wtState{branch: "side", dirty: true}
	fd.states["/elsewhere/foreign-wt"] = wtState{branch: "feature", dirty: true}

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	// One row per unmerged linked worktree — the shared registry enumerated
	// once, never once per project.
	if got := strings.Count(out, "(unmanaged)"); got != 2 {
		t.Fatalf("want each of the two dirty worktrees exactly once, got %d rows:\n%s", got, out)
	}
	// Count the checkout paths only in the (unmanaged) enumeration rows: projwt,
	// a project node whose dir IS /repo-wt, also carries a (location) row for it,
	// and that legitimate second mention must not read as a dedupe failure.
	unmanagedMentions := func(path string) int {
		n := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "(unmanaged)") && strings.Contains(line, path) {
				n++
			}
		}
		return n
	}
	if unmanagedMentions("/repo-wt") != 1 {
		t.Fatalf("the dirty worktree projwt stands on must enumerate exactly once:\n%s", out)
	}
	if unmanagedMentions("/elsewhere/foreign-wt") != 1 {
		t.Fatalf("the no-project foreign worktree must enumerate exactly once:\n%s", out)
	}
	// The rows hang under the MAIN-checkout project: in the rendered tree
	// children directly follow their parent, so each (unmanaged) line follows
	// projmain's line, projmain's own (location) continuation line, or a sibling
	// (unmanaged) line.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "(unmanaged)") {
			if i == 0 || (!strings.Contains(lines[i-1], "projmain") && !strings.Contains(lines[i-1], "(location)") && !strings.Contains(lines[i-1], "(unmanaged)")) {
				t.Fatalf("rows must hang under projmain (the main checkout), got:\n%s", out)
			}
		}
	}
}

// CONTRACT: repo grouping is symlink-normalized. Two project nodes reaching
// ONE repository through differing symlinked paths land in the same group —
// the registry enumerates once, one row, under the main-checkout project.
func TestLsForeignWorktreesGroupAcrossSymlinkedPaths(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "projreal/dabs-node.json": []byte(`{"id":"projreal","kind":"project","dir":"/vol/repo","created":"1"}`),
		base + "projlink/dabs-node.json": []byte(`{"id":"projlink","kind":"project","dir":"/link/repo","created":"2"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"projreal", "projlink"}}
	spaceHeld(fd, "projreal", "held")
	spaceHeld(fd, "projlink", "held")
	// git answers each path with a common dir spelled through that path …
	fd.commondir = map[string]string{"/vol/repo": "/vol/repo/.git", "/link/repo": "/link/repo/.git"}
	// … and the symlink resolution maps both spellings to one canonical repo.
	fd.symlinks = map[string]string{
		"/vol/repo":       "/vol/repo",
		"/link/repo":      "/vol/repo",
		"/vol/repo/.git":  "/vol/repo/.git",
		"/link/repo/.git": "/vol/repo/.git",
	}
	// Both spellings answer with the same registry, so a split group would
	// enumerate it twice and render two rows.
	shared := []string{"/elsewhere/foreign-wt"}
	fd.foreign = map[string][]string{"/vol/repo": shared, "/link/repo": shared}
	fd.states["/elsewhere/foreign-wt"] = wtState{branch: "feature", dirty: true}

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})
	if got := strings.Count(out, "(unmanaged)"); got != 1 {
		t.Fatalf("symlinked spellings of one repo must group — want one row, got %d:\n%s", got, out)
	}
	// The row hangs under the main-checkout project (canonically /vol/repo),
	// directly after projreal's line or its (location) continuation line.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "(unmanaged)") {
			if i == 0 || (!strings.Contains(lines[i-1], "projreal") && !strings.Contains(lines[i-1], "(location)")) {
				t.Fatalf("the row must hang under the canonical main-checkout project, got:\n%s", out)
			}
		}
	}
}

// CONTRACT: reaping an externally-managed worktree marker never touches the
// checkout's bytes — its Dir is the user's, exactly like a project's — and
// never deregisters the worktree from git. Afterwards the still-dirty checkout
// renders exactly once, as an `(unmanaged)` row under the repo's project.
func TestRmMarkerLeavesCheckoutAndFallsBackToUnmanaged(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj1/dabs-node.json": []byte(`{"id":"proj1","kind":"project","dir":"/repo","created":"1"}`),
		base + "marker1/dabs-node.json": []byte(
			`{"id":"marker1","kind":"worktree","parent":"proj1","dir":"/repo-wt","recipe":"r","created":"2"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj1", "marker1"}}
	spaceHeld(fd, "proj1", "held")
	fd.foreign = map[string][]string{"/repo": {"/repo-wt"}}
	fd.states["/repo-wt"] = wtState{branch: "side", dirty: true}
	r := newReal("", fd, &fakeDriver{})

	out := captureStdout(t, func() {
		if err := r.Rm(params.Rm{Node: "marker1", Yes: true}); err != nil {
			t.Fatalf("rm marker1: %v", err)
		}
	})
	if !strings.Contains(out, "removed") {
		t.Fatalf("rm must reap the marker record:\n%s", out)
	}
	for _, p := range fd.rmAll {
		if p == "/repo-wt" || strings.HasPrefix(p, "/repo-wt/") {
			t.Fatalf("rm followed the marker's Dir and deleted the checkout: %v", fd.rmAll)
		}
	}
	if len(fd.removed) != 0 {
		t.Fatalf("rm must not deregister an externally-managed worktree from git: %v", fd.removed)
	}

	out = captureStdout(t, func() {
		if err := r.Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls after the reap: %v", err)
		}
	})
	if got := strings.Count(out, "(unmanaged)"); got != 1 {
		t.Fatalf("the still-dirty checkout must fall back to exactly one (unmanaged) row, got %d:\n%s", got, out)
	}
	if strings.Count(out, "/repo-wt") != 1 {
		t.Fatalf("the checkout must render exactly once:\n%s", out)
	}
}

// CONTRACT: `ls` with a driver that cannot answer still renders the tree — the
// driver's error is a single stderr warning, and each box whose state could
// not be checked shows `(error: no driver)` instead of live/gone. The box here
// has EMPTY spaces: unconfirmed is not confirmed-dead, so it stays in the
// DEFAULT listing (potentially active) rather than vanishing into --inactive.
func TestLsRendersWithFailingDriver(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "boxy", "inst-b")
	drv := &fakeDriver{lsErrOnce: errors.New("bwrap: 'bwrap' not found")}
	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Ls(params.Ls{}); err != nil {
			t.Fatalf("ls with a failing driver: %v", err)
		}
	})
	if !strings.Contains(out, "boxy") {
		t.Fatalf("unconfirmed box missing from the DEFAULT listing, got:\n%s", out)
	}
	if !strings.Contains(out, "(error: no driver)") {
		t.Fatalf("unconfirmed box must show the no-driver state, got:\n%s", out)
	}
}

// CONTRACT: the --inactive view is confirmed-dead history. With an incomplete
// drivers' answer, an unconfirmed box is NOT classified inactive — and the
// same rule keeps `rm --inactive` from sweeping it on a guess.
func TestUnknownBoxIsNotInactive(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "boxy", "inst-b")
	drv := &fakeDriver{lsErrOnce: errors.New("bwrap: 'bwrap' not found")}
	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Ls(params.Ls{Inactive: true}); err != nil {
			t.Fatalf("ls --inactive: %v", err)
		}
	})
	if strings.Contains(out, "boxy") {
		t.Fatalf("unconfirmed box must not be listed as inactive, got:\n%s", out)
	}
}
