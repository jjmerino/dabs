package actions_test

// Tests for `dabs down` name-resolution safety. The footgun these guard
// against: a prefix (or an empty name) that matches several boxes reaping ALL
// of them. Policy: exactly one match downs it; more than one is refused unless
// --multiple; an empty/blank name matches nothing.

import (
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

func twoBoxes() *fakeDriver {
	return &fakeDriver{infos: []sandbox.Info{
		{Name: "demo-a1b2", Status: "running", Driver: "fake"},
		{Name: "demo-c3d4", Status: "running", Driver: "fake"},
		{Name: "other-e5f6", Status: "running", Driver: "fake"},
	}}
}

// CONTRACT: a prefix matching exactly one instance downs that one.
func TestDownExactlyOneMatchDownsIt(t *testing.T) {
	drv := twoBoxes()
	if err := newReal("", baseData(), drv).Down(params.Down{Instance: "demo-a"}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "demo-a1b2" {
		t.Fatalf("want [demo-a1b2], got %v", drv.downs)
	}
}

// CONTRACT: a full exact name downs it even when it's a prefix of others.
func TestDownFullExactNameDownsOne(t *testing.T) {
	drv := &fakeDriver{infos: []sandbox.Info{
		{Name: "demo", Status: "running", Driver: "fake"},
		{Name: "demo-a1b2", Status: "running", Driver: "fake"},
	}}
	if err := newReal("", baseData(), drv).Down(params.Down{Instance: "demo"}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "demo" {
		t.Fatalf("want [demo], got %v", drv.downs)
	}
}

// CONTRACT: a prefix matching >1 instance without --multiple errors and downs
// nothing — the core footgun fix.
func TestDownMultipleMatchesWithoutFlagRefuses(t *testing.T) {
	drv := twoBoxes()
	err := newReal("", baseData(), drv).Down(params.Down{Instance: "demo"})
	if err == nil {
		t.Fatal("want an error refusing the multi-match, got nil")
	}
	if len(drv.downs) != 0 {
		t.Fatalf("must down NOTHING on refusal, downed %v", drv.downs)
	}
}

// CONTRACT: --force does NOT authorize multi-match reaping; only --multiple does.
func TestDownForceAloneDoesNotAuthorizeMulti(t *testing.T) {
	drv := twoBoxes()
	err := newReal("", baseData(), drv).Down(params.Down{Instance: "demo", Force: true})
	if err == nil {
		t.Fatal("want an error: --force alone must not reap multiple, got nil")
	}
	if len(drv.downs) != 0 {
		t.Fatalf("must down NOTHING, downed %v", drv.downs)
	}
}

// CONTRACT: --multiple downs every match.
func TestDownMultipleFlagDownsAll(t *testing.T) {
	drv := twoBoxes()
	if err := newReal("", baseData(), drv).Down(params.Down{Instance: "demo", Multiple: true}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(drv.downs) != 2 {
		t.Fatalf("want both demo boxes downed, got %v", drv.downs)
	}
}

// CONTRACT: an empty name matches nothing (never "all"), even with the flags
// that would otherwise authorize acting on many.
func TestDownEmptyNameErrorsAndDownsNothing(t *testing.T) {
	for _, p := range []params.Down{
		{Instance: ""},
		{Instance: "   "},
		{Instance: "", Multiple: true, Force: true},
	} {
		drv := twoBoxes()
		err := newReal("", baseData(), drv).Down(p)
		if err == nil {
			t.Fatalf("want an error for name %q, got nil", p.Instance)
		}
		if len(drv.downs) != 0 {
			t.Fatalf("empty name must down NOTHING, downed %v", drv.downs)
		}
	}
}

// CONTRACT: --dry previews matches and downs nothing, regardless of count.
func TestDownDryDownsNothing(t *testing.T) {
	drv := twoBoxes()
	if err := newReal("", baseData(), drv).Down(params.Down{Instance: "demo", Dry: true}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(drv.downs) != 0 {
		t.Fatalf("--dry must down NOTHING, downed %v", drv.downs)
	}
}

// CONTRACT: `down` applies each node space's promise. tmp/ goes without asking,
// volume/ is never touched, and a non-empty ephemeral/ is not deleted behind the
// user's back — down refuses unless they consent (or --force). The space, not the
// recipe, decides: convention, so `down` never has to interpret intent.
func TestDownReapsTmpKeepsVolumeAndAsksBeforeEphemeral(t *testing.T) {
	const inst, node = "img-inst", "r-abcd1234"
	base := "/home/t/.dabs/nodes/" + node

	newFixture := func(ephFiles []string) (*fakeData, *fakeDriver) {
		fd := baseData()
		fd.files = map[string][]byte{}
		fd.dirs = map[string][]string{}
		fd.files[base+"/dabs-node.json"] = []byte(
			`{"id":"` + node + `","kind":"box","instance":"` + inst + `","recipe":"r"}`)
		fd.dirs["/home/t/.dabs/nodes"] = []string{node}
		fd.dirs[base+"/ephemeral"] = ephFiles
		drv := &fakeDriver{infos: []sandbox.Info{{Name: inst, Status: "running"}}}
		return fd, drv
	}

	t.Run("empty ephemeral: reaps tmp and ephemeral, never volume", func(t *testing.T) {
		fd, drv := newFixture(nil)
		if err := newReal("", fd, drv).Down(params.Down{Instance: inst, Force: true}); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if len(drv.downs) != 1 {
			t.Fatalf("box not downed: %v", drv.downs)
		}
		wantGone := []string{base + "/tmp", base + "/ephemeral"}
		for _, w := range wantGone {
			found := false
			for _, got := range fd.rmAll {
				if got == w {
					found = true
				}
			}
			if !found {
				t.Errorf("down did not reap %s; removed=%v", w, fd.rmAll)
			}
		}
		for _, got := range fd.rmAll {
			if got == base+"/volume" {
				t.Errorf("down deleted the volume space, which must survive: %v", fd.rmAll)
			}
		}
	})

	t.Run("non-empty ephemeral, refused: box stays up and nothing is deleted", func(t *testing.T) {
		fd, drv := newFixture([]string{"worktree"})
		r := newReal("", fd, drv).WithConfirm(func(string) bool { return false }) // the user says no
		err := r.Down(params.Down{Instance: inst})
		if err == nil {
			t.Fatal("want an error when the user declines")
		}
		if len(drv.downs) != 0 {
			t.Errorf("box was downed despite the refusal: %v", drv.downs)
		}
		for _, got := range fd.rmAll {
			if got == base+"/ephemeral" {
				t.Errorf("ephemeral deleted after a refusal: %v", fd.rmAll)
			}
		}
	})
}
