package actions_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

const (
	nodeBase = "/home/t/.dabs/nodes"
	logPath  = "/home/t/.dabs/log.jsonl"
)

// seedWorktreeNode makes fd look as if dabs had provisioned a worktree node:
// the node is listed under nodes/, its record marks it a worktree (with the
// recipe that made it), and its data/ is the checkout git reports state for.
// This mirrors exactly what addWorktree writes.
func seedWorktreeNode(fd *fakeData, id string, st wtState) string {
	data := nodeBase + "/" + id + "/data"
	if fd.dirs == nil {
		fd.dirs = map[string][]string{}
	}
	fd.dirs[nodeBase] = append(fd.dirs[nodeBase], id)
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	branch := st.branch
	if branch == "" {
		branch = "dabs/" + id
	}
	fd.files[nodeBase+"/"+id+"/dabs-node.json"] = []byte(
		`{"id":"` + id + `","recipe":"r","created":"t",` +
			`"worktree":{"branch":"` + branch + `","repo":"/repo"}}`)
	if fd.states == nil {
		fd.states = map[string]wtState{}
	}
	fd.states[data] = st
	if fd.commondir == nil {
		fd.commondir = map[string]string{}
	}
	fd.commondir[data] = "/repo/.git"
	// The checkout is really on disk — cast rewrites a `worktree:` source into a
	// mount of this path, and mounts are validated for existence.
	fd.exists[data] = true
	fd.isDir[data] = true
	return data
}

// seedStray drops an entry under nodes/ that dabs never wrote a record for.
func seedStray(fd *fakeData, name string) {
	if fd.dirs == nil {
		fd.dirs = map[string][]string{}
	}
	fd.dirs[nodeBase] = append(fd.dirs[nodeBase], name)
}

// CONTRACT: a worktree with unreviewed work (uncommitted OR commits ahead) must
// NOT be removed without --force — losing an agent's work needs approval.
func TestWorktreeRmRefusesUnreviewedWork(t *testing.T) {
	for _, s := range []wtState{{dirty: true}, {ahead: 2}} {
		fd := baseData()
		seedWorktreeNode(fd, "wt1", s)
		err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "rm", Name: "wt1"})
		if err == nil || !strings.Contains(err.Error(), "unreviewed work") {
			t.Fatalf("state %+v: want refusal, got %v", s, err)
		}
		if len(fd.removed) != 0 {
			t.Fatalf("state %+v: removed unreviewed work: %v", s, fd.removed)
		}
	}
}

// CONTRACT: --force is the approval — it discards even unreviewed work.
func TestWorktreeRmForceDiscards(t *testing.T) {
	fd := baseData()
	data := seedWorktreeNode(fd, "wt1", wtState{dirty: true, ahead: 3})
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "rm", Name: "wt1", Force: true}); err != nil {
		t.Fatalf("force rm: %v", err)
	}
	if len(fd.removed) != 1 || fd.removed[0] != data {
		t.Fatalf("force rm did not remove the checkout: %v", fd.removed)
	}
}

// CONTRACT: a clean worktree removes without --force.
func TestWorktreeRmCleanNeedsNoForce(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "clean", wtState{branch: "dabs/x"}) // no work
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "rm", Name: "clean"}); err != nil {
		t.Fatalf("rm clean: %v", err)
	}
	if len(fd.removed) != 1 {
		t.Fatalf("clean worktree not removed: %v", fd.removed)
	}
}

// CONTRACT: prune reaps clean worktrees but keeps the ones with work.
func TestWorktreePruneKeepsWork(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "clean1", wtState{})
	seedWorktreeNode(fd, "dirty1", wtState{ahead: 1})
	seedWorktreeNode(fd, "clean2", wtState{})
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "prune"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(fd.removed) != 2 {
		t.Fatalf("prune should remove the 2 clean worktrees, removed %v", fd.removed)
	}
	for _, r := range fd.removed {
		if strings.Contains(r, "dirty1") {
			t.Fatalf("prune discarded a worktree with work: %s", r)
		}
	}
}

// CONTRACT: prune --force reaps everything, work and all.
func TestWorktreePruneForceReapsAll(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "clean1", wtState{})
	seedWorktreeNode(fd, "dirty1", wtState{dirty: true})
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "prune", Force: true}); err != nil {
		t.Fatalf("prune --force: %v", err)
	}
	if len(fd.removed) != 2 {
		t.Fatalf("prune --force should remove all, removed %v", fd.removed)
	}
}

// CONTRACT: `worktrees ls` prints NAME | WORKTREE | STATE | DETAIL — the WORKTREE
// column is the ABSOLUTE path to the node's data, the branch and the recipe that
// made it are folded into DETAIL, and per-worktree box liveness is read from the
// journal (intersected with the live fleet).
func TestWorktreeLsColumnsAndLiveness(t *testing.T) {
	fd := baseData()
	live := seedWorktreeNode(fd, "wtlive", wtState{branch: "dabs/aa", dirty: true})
	seedWorktreeNode(fd, "wtdead", wtState{branch: "dabs/bb"})
	// A live box for wtlive (up, no matching down); wtdead's box came down.
	fd.files[logPath] = []byte(
		`{"event":"up","ts":"t1","instance":"box-live","worktree":"wtlive","path":"` + live + `","recipe":"r"}` + "\n" +
			`{"event":"up","ts":"t2","instance":"box-dead","worktree":"wtdead"}` + "\n" +
			`{"event":"down","ts":"t3","instance":"box-dead","worktree":"wtdead"}` + "\n")
	// The fleet actually has box-live running (liveness is journal ∩ fleet).
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "box-live"}}}
	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Worktrees(params.Worktrees{Sub: "ls"}); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "WORKTREE") ||
		!strings.Contains(out, "STATE") || !strings.Contains(out, "DETAIL") {
		t.Fatalf("header not NAME|WORKTREE|STATE|DETAIL:\n%s", out)
	}
	if strings.Contains(out, "BRANCH") {
		t.Fatalf("BRANCH must be folded into DETAIL, not a column:\n%s", out)
	}
	// WORKTREE is the absolute data path; branch AND the provisioning recipe are
	// in DETAIL — the record is what makes provenance printable at all.
	if !strings.Contains(out, live) || !strings.Contains(out, "branch dabs/aa") {
		t.Fatalf("missing abs data path / branch in detail:\n%s", out)
	}
	if !strings.Contains(out, "recipe r") {
		t.Fatalf("DETAIL must show the recipe that provisioned it:\n%s", out)
	}
	if !strings.Contains(out, "box box-live live") {
		t.Fatalf("wtlive should show its live box:\n%s", out)
	}
	if !strings.Contains(out, "no box") {
		t.Fatalf("wtdead should show no box:\n%s", out)
	}
}

// CONTRACT: dabs lists only what it PROVISIONED. An entry under nodes/ with no
// node record was not written by dabs — it is not a node, so ls silently skips
// it (no bogus row, no "unreadable" error) while real worktree nodes still list.
func TestWorktreeLsSkipsStrayEntries(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "real1", wtState{branch: "dabs/aa"})
	seedStray(fd, ".DS_Store")
	seedStray(fd, "scratch")
	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "ls"}); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	if !strings.Contains(out, "real1") || !strings.Contains(out, "branch dabs/aa") {
		t.Fatalf("real worktree node should list:\n%s", out)
	}
	if strings.Contains(out, ".DS_Store") || strings.Contains(out, "scratch") {
		t.Fatalf("a stray entry leaked into the listing:\n%s", out)
	}
	if strings.Contains(out, "unreadable") {
		t.Fatalf("stray entry produced an error row instead of being skipped:\n%s", out)
	}
}

// CONTRACT: prune enumerates only nodes dabs provisioned — it must never try to
// reap a stray entry that happens to sit under nodes/.
func TestWorktreePruneSkipsStrayEntries(t *testing.T) {
	fd := baseData()
	data := seedWorktreeNode(fd, "real1", wtState{}) // clean → reaped
	seedStray(fd, ".DS_Store")
	seedStray(fd, "scratch")
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "prune"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(fd.removed) != 1 || fd.removed[0] != data {
		t.Fatalf("prune should reap only the real node, removed %v", fd.removed)
	}
}
