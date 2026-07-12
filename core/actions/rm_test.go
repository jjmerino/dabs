package actions_test

// Tests for `dabs rm` name-resolution ergonomics:
//   - a no-match reap is idempotent (exit 0), the same as `dabs down`;
//   - --multiple reaps every prefix match, and is REQUIRED when a name matches
//     more than one node (mirroring `down`).

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// seedBoxNode makes fd look as if dabs had provisioned a box node: a record
// under nodes/ marking it a box bound to the given instance.
func seedBoxNode(fd *fakeData, id, instance string) {
	if fd.dirs == nil {
		fd.dirs = map[string][]string{}
	}
	fd.dirs[nodeBase] = append(fd.dirs[nodeBase], id)
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	fd.files[nodeBase+"/"+id+"/dabs-node.json"] = []byte(
		`{"id":"` + id + `","kind":"box","instance":"` + instance + `","recipe":"r","created":"t"}`)
}

// CONTRACT (B15): naming a node that isn't there is NOT an error — `rm` matches
// `down`'s idempotent behaviour, so a cleanup script gets the same exit status
// from both whether or not the node still exists.
func TestRmMissingIsNotError(t *testing.T) {
	drv := &fakeDriver{}
	err := newReal("", baseData(), drv).Rm(params.Rm{Node: "ghost"})
	if err != nil {
		t.Fatalf("rm of a missing node = %v, want nil (idempotent, like down)", err)
	}
	if len(drv.downs) != 0 {
		t.Fatalf("rm of a missing node downed something: %v", drv.downs)
	}
}

// CONTRACT (B14): a prefix matching more than one node is REFUSED without
// --multiple, and reaps nothing.
func TestRmMultipleMatchesWithoutFlagRefuses(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "demo-aaaa", "inst-a")
	seedBoxNode(fd, "demo-bbbb", "inst-b")
	drv := &fakeDriver{infos: []sandbox.Info{
		{Name: "inst-a", Status: "running"},
		{Name: "inst-b", Status: "running"},
	}}
	// -y removes the cascade prompt as a reason to stop, so the ONLY thing that
	// can refuse here is the multi-match guard itself.
	err := newReal("", fd, drv).Rm(params.Rm{Node: "demo", Yes: true})
	if err == nil {
		t.Fatal("want an error refusing the multi-match, got nil")
	}
	if len(drv.downs) != 0 {
		t.Fatalf("must reap NOTHING on refusal, downed %v", drv.downs)
	}
}

// CONTRACT (B14): --multiple reaps every match — the box behind each matched
// node is brought down.
func TestRmMultipleFlagReapsAll(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "demo-aaaa", "inst-a")
	seedBoxNode(fd, "demo-bbbb", "inst-b")
	drv := &fakeDriver{infos: []sandbox.Info{
		{Name: "inst-a", Status: "running"},
		{Name: "inst-b", Status: "running"},
	}}
	// -y consents to the cascade prompt (two separate nodes → the doomed set is >1).
	if err := newReal("", fd, drv).Rm(params.Rm{Node: "demo", Multiple: true, Yes: true}); err != nil {
		t.Fatalf("rm --multiple: %v", err)
	}
	got := strings.Join(drv.downs, ",")
	if !strings.Contains(got, "inst-a") || !strings.Contains(got, "inst-b") {
		t.Fatalf("want both boxes downed, got %v", drv.downs)
	}
}
