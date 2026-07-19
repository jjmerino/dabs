package actions_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// CONTRACT: `cd` resolves a node's WORKING PLACE per kind — a worktree's
// checkout, a project's source repo — and falls back to the node dir for a box.
func TestCdPerKindWorkingPlace(t *testing.T) {
	fd := baseData()
	data := seedWorktreeNode(fd, "wt-abcd", wtState{branch: "b"})
	r := newReal("", fd, &fakeDriver{})

	// A worktree cd's into its checkout, not the node record dir.
	out := captureStdout(t, func() {
		if err := r.Cd(params.Cd{Node: "wt-abcd"}); err != nil {
			t.Fatalf("cd worktree: %v", err)
		}
	})
	if strings.TrimSpace(out) != data {
		t.Fatalf("cd worktree printed %q, want the checkout %s", out, data)
	}

	// A project cd's into the repo the user ran dabs from, never the node dir.
	fd.dirs[nodeBase] = append(fd.dirs[nodeBase], "work-aaaa")
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	fd.files[nodeBase+"/work-aaaa/dabs-node.json"] = []byte(`{"id":"work-aaaa","kind":"project","dir":"/cwd","recipe":"r","created":"t"}`)
	out = captureStdout(t, func() {
		if err := r.Cd(params.Cd{Node: "work-aaaa"}); err != nil {
			t.Fatalf("cd project: %v", err)
		}
	})
	if strings.TrimSpace(out) != "/cwd" {
		t.Fatalf("cd project printed %q, want the source repo /cwd", out)
	}

	// A box has no working place of its own — it falls back to its node dir.
	seedBoxNode(fd, "boxy", "inst-b")
	out = captureStdout(t, func() {
		if err := r.Cd(params.Cd{Node: "boxy"}); err != nil {
			t.Fatalf("cd box: %v", err)
		}
	})
	if strings.TrimSpace(out) != nodeBase+"/boxy" {
		t.Fatalf("cd box printed %q, want the node dir %s", out, nodeBase+"/boxy")
	}
}
