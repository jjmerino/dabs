package actions_test

import (
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
	// Only the checkout that exists is read; both resolve to ephemeral/worktree.
	fd.states[base+dirtyID+"/ephemeral/worktree"] = wtState{branch: "dabs/" + dirtyID, dirty: true}
	fd.states[base+cleanID+"/ephemeral/worktree"] = wtState{branch: "dabs/" + cleanID}

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, dirtyID) && !strings.Contains(line, "unmerged"):
			t.Errorf("worktree holding work is not marked unmerged:\n%s", line)
		case strings.Contains(line, cleanID) && strings.Contains(line, "unmerged"):
			t.Errorf("clean worktree is marked unmerged; the mark would mean nothing:\n%s", line)
		}
	}
	if !strings.Contains(out, "has work") {
		t.Errorf("the heading does not say what the * means:\n%s", out)
	}
}
