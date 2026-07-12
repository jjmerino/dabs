package actions_test

// Test for `dabs ls --all` rendering (B16): a project/workdir chain that has
// BOTH a live and a gone box must appear ONCE — under the driver heading for
// the live box — not a second time in the "no box" section carried by the gone
// box.

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

func TestLsAllDoesNotDoubleRenderSharedChain(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{}
	fd.dirs = map[string][]string{}
	base := nodeBase + "/"
	// A project with two boxes under it: one live, one gone.
	fd.files[base+"proj-1/dabs-node.json"] = []byte(
		`{"id":"proj-1","kind":"project","dir":"/repo","recipe":"r","created":"1"}`)
	fd.files[base+"box-live/dabs-node.json"] = []byte(
		`{"id":"box-live","kind":"box","parent":"proj-1","instance":"inst-live","recipe":"r","created":"2"}`)
	fd.files[base+"box-gone/dabs-node.json"] = []byte(
		`{"id":"box-gone","kind":"box","parent":"proj-1","instance":"inst-gone","recipe":"r","created":"3"}`)
	fd.dirs[nodeBase] = []string{"proj-1", "box-live", "box-gone"}
	// Only inst-live is held by a driver; inst-gone is archived.
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-live", Status: "running"}}}

	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Ls(params.Ls{All: true}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	if n := strings.Count(out, "proj-1"); n != 1 {
		t.Fatalf("project chain rendered %d times, want 1:\n%s", n, out)
	}
	// The gone box is still shown (once), under the "no box" section.
	if n := strings.Count(out, "box-gone"); n != 1 {
		t.Fatalf("gone box rendered %d times, want 1:\n%s", n, out)
	}
}
