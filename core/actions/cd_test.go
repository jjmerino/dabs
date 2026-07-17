package actions_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// CONTRACT: `cd` prints the NODE dir for every kind — one uniform rule. The
// spaces are its literal subdirectories; no flag reaches inside.
func TestCdUniformNodeDir(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "wt-abcd", wtState{branch: "b"})
	r := newReal("", fd, &fakeDriver{})

	out := captureStdout(t, func() {
		if err := r.Cd(params.Cd{Node: "wt-abcd"}); err != nil {
			t.Fatalf("cd worktree: %v", err)
		}
	})
	if strings.TrimSpace(out) != nodeBase+"/wt-abcd" {
		t.Fatalf("bare cd printed %q, want the node dir %s", out, nodeBase+"/wt-abcd")
	}

	// A project too: bare cd prints ITS node dir, never the repo the user runs
	// dabs from.
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
	if strings.TrimSpace(out) != nodeBase+"/work-aaaa" {
		t.Fatalf("bare cd on a project printed %q, want the node dir %s", out, nodeBase+"/work-aaaa")
	}
}
