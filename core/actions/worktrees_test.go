package actions_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

const wtBase = "/home/t/.dabs/worktrees"

// CONTRACT: a worktree with unreviewed work (uncommitted OR commits ahead) must
// NOT be removed without --force — losing an agent's work needs approval.
func TestWorktreeRmRefusesUnreviewedWork(t *testing.T) {
	for _, s := range []wtState{{dirty: true}, {ahead: 2}} {
		fd := baseData()
		fd.states = map[string]wtState{wtBase + "/wt1": s}
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
	fd.states = map[string]wtState{wtBase + "/wt1": {dirty: true, ahead: 3}}
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "rm", Name: "wt1", Force: true}); err != nil {
		t.Fatalf("force rm: %v", err)
	}
	if len(fd.removed) != 1 || fd.removed[0] != wtBase+"/wt1" {
		t.Fatalf("force rm did not remove: %v", fd.removed)
	}
}

// CONTRACT: a clean worktree removes without --force.
func TestWorktreeRmCleanNeedsNoForce(t *testing.T) {
	fd := baseData()
	fd.states = map[string]wtState{wtBase + "/clean": {branch: "dabs/x"}} // no work
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
	fd.dirs = map[string][]string{wtBase: {"clean1", "dirty1", "clean2"}}
	fd.states = map[string]wtState{
		wtBase + "/clean1": {},
		wtBase + "/dirty1": {ahead: 1},
		wtBase + "/clean2": {},
	}
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "prune"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(fd.removed) != 2 {
		t.Fatalf("prune should remove the 2 clean worktrees, removed %v", fd.removed)
	}
	for _, r := range fd.removed {
		if strings.HasSuffix(r, "dirty1") {
			t.Fatalf("prune discarded a worktree with work: %s", r)
		}
	}
}

// CONTRACT: prune --force reaps everything, work and all.
func TestWorktreePruneForceReapsAll(t *testing.T) {
	fd := baseData()
	fd.dirs = map[string][]string{wtBase: {"clean1", "dirty1"}}
	fd.states = map[string]wtState{wtBase + "/dirty1": {dirty: true}}
	if err := newReal("", fd, &fakeDriver{}).Worktrees(params.Worktrees{Sub: "prune", Force: true}); err != nil {
		t.Fatalf("prune --force: %v", err)
	}
	if len(fd.removed) != 2 {
		t.Fatalf("prune --force should remove all, removed %v", fd.removed)
	}
}

// CONTRACT: `worktrees ls` prints NAME | WORKTREE | STATE | DETAIL — the WORKTREE
// column is the ABSOLUTE path, the branch is folded into DETAIL, and per-worktree
// box liveness is read from the log.
func TestWorktreeLsColumnsAndLiveness(t *testing.T) {
	fd := baseData()
	// The journal lives in the worktrees dir too; ls must not treat it as one.
	fd.dirs = map[string][]string{wtBase: {"log.jsonl", "wtlive", "wtdead"}}
	fd.states = map[string]wtState{
		wtBase + "/wtlive": {branch: "dabs/aa", dirty: true},
		wtBase + "/wtdead": {branch: "dabs/bb"},
	}
	// A live box for wtlive (up, no matching down); wtdead's box came down.
	fd.files = map[string][]byte{
		wtBase + "/log.jsonl": []byte(
			`{"event":"up","ts":"t1","instance":"box-live","worktree":"wtlive","path":"` + wtBase + `/wtlive","recipe":"r"}` + "\n" +
				`{"event":"up","ts":"t2","instance":"box-dead","worktree":"wtdead"}` + "\n" +
				`{"event":"down","ts":"t3","instance":"box-dead","worktree":"wtdead"}` + "\n"),
	}
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
	// The WORKTREE column shows the absolute path, and the branch is in DETAIL.
	if !strings.Contains(out, wtBase+"/wtlive") || !strings.Contains(out, "branch dabs/aa") {
		t.Fatalf("missing abs path / branch in detail:\n%s", out)
	}
	// Liveness: wtlive has a live box; wtdead has none.
	if !strings.Contains(out, "box box-live live") {
		t.Fatalf("wtlive should show its live box:\n%s", out)
	}
	if !strings.Contains(out, "no box") {
		t.Fatalf("wtdead should show no box:\n%s", out)
	}
	// The journal file must never appear as a worktree row.
	if strings.Contains(out, "log.jsonl") {
		t.Fatalf("log.jsonl leaked into the worktrees listing:\n%s", out)
	}
}
