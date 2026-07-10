package actions_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
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
