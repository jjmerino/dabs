package actions_test

// Tests for `dabs rm`, the single reaper (it absorbed `down`):
//   - a no-match reap is an ERROR naming the miss, like cd/exec;
//   - --multiple reaps every prefix match, and is REQUIRED when a name matches
//     more than one node;
//   - a reap that would stop a LIVE box or lose held data needs consent (-y),
//     and without it non-interactively it keeps everything and exits NONZERO;
//   - --keep stops the box but keeps its record only when files remain;
//   - --inactive sweeps the inactive subtrees (empty markers) and nothing else.

import (
	"fmt"
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

// CONTRACT (B15): naming a node that isn't there is an ERROR that names the
// miss and points at `dabs ls` — the same answer cd and exec give for the same
// situation, so a typo cannot read as a clean reap — and it reaps nothing.
func TestRmMissingIsError(t *testing.T) {
	drv := &fakeDriver{}
	err := newReal("", baseData(), drv).Rm(params.Rm{Node: "ghost"})
	if err == nil {
		t.Fatal("rm of a missing node = nil, want an error naming the miss")
	}
	if !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "dabs ls") {
		t.Fatalf("error should name the node and point at dabs ls, got %v", err)
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

// spaceHeld makes a node's space read as holding files, so spaceHolds — the ONE
// check the reap preview and the reap itself both use — reports it non-empty.
func spaceHeld(fd *fakeData, id, space string) {
	if fd.dirs == nil {
		fd.dirs = map[string][]string{}
	}
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	base := nodeBase + "/" + id + "/" + space
	fd.dirs[base] = []string{"a-file"}
	fd.files[base+"/a-file"] = []byte("x") // a real file, not just a non-empty listing
}

func rmAllHas(fd *fakeData, p string) bool {
	for _, x := range fd.rmAll {
		if x == p {
			return true
		}
	}
	return false
}

// CONTRACT (E2-2): a node that holds data (here a held space) is NOT reaped
// without consent. Non-interactively there is nobody to ask, so the node is KEPT,
// nothing is removed, and rm exits NONZERO — a script must see the reap did not
// happen rather than read exit 0 as "gone".
func TestRmHeldSpaceWithoutConsentKeepsNodeAndErrors(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "hold-aaaa", "inst-a")
	spaceHeld(fd, "hold-aaaa", "held")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-a", Status: "running"}}}

	var err error
	out := captureStdout(t, func() {
		err = newReal("", fd, drv).Rm(params.Rm{Node: "hold-aaaa"})
	})
	if err == nil {
		t.Fatal("held data without -y non-interactively must error, got nil")
	}
	if !strings.Contains(out, "a held space") {
		t.Errorf("held-space should be previewed; got:\n%s", out)
	}
	if strings.Contains(out, "removed") {
		t.Errorf("a kept node must NOT be removed; got:\n%s", out)
	}
	if len(drv.downs) != 0 {
		t.Errorf("a kept node's box must NOT be stopped; downed %v", drv.downs)
	}
	if rmAllHas(fd, nodeBase+"/hold-aaaa") || rmAllHas(fd, nodeBase+"/hold-aaaa/held") {
		t.Errorf("reaped despite refusal: %v", fd.rmAll)
	}
}

// CONTRACT (E2-1): `rm` of a LIVE box without -y, non-interactively, does NOT
// stop or remove it and exits NONZERO — even with empty spaces. Stopping a
// running box is itself a loss that needs consent. (This is what a bare
// single-node `rm` used to do silently with exit 0.)
func TestRmLiveBoxWithoutConsentKeepsAndErrors(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "live-aaaa", "inst-a") // no held spaces — only the box is live
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-a", Status: "running"}}}

	var err error
	out := captureStdout(t, func() {
		err = newReal("", fd, drv).Rm(params.Rm{Node: "live-aaaa"})
	})
	if err == nil {
		t.Fatal("rm of a live box without -y must error non-interactively, got nil")
	}
	if len(drv.downs) != 0 {
		t.Errorf("a refused rm must NOT stop the box; downed %v", drv.downs)
	}
	if rmAllHas(fd, nodeBase+"/live-aaaa") {
		t.Errorf("a refused rm must NOT remove the node: %v", fd.rmAll)
	}
	if !strings.Contains(out, "reaps") {
		t.Errorf("a refused rm should preview what it would take; got:\n%s", out)
	}
}

// CONTRACT: --keep on a box that LEFT FILES BEHIND stops the box but keeps its
// node record — there is something to point at (a leftover volume here, which
// --keep never reaps without --volume). The kept record reads as gone-with-files.
func TestRmKeepBoxWithFilesKeepsNode(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "keep-aaaa", "inst-a")
	spaceHeld(fd, "keep-aaaa", "volume") // a leftover the box wrote — kept without --volume
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-a", Status: "running"}}}

	if err := newReal("", fd, drv).Rm(params.Rm{Node: "keep-aaaa", Keep: true}); err != nil {
		t.Fatalf("rm --keep: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "inst-a" {
		t.Fatalf("--keep must stop the box, downed %v", drv.downs)
	}
	if rmAllHas(fd, nodeBase+"/keep-aaaa") {
		t.Errorf("--keep on a box with leftover files must NOT remove the node dir: %v", fd.rmAll)
	}
}

// CONTRACT: bringing a box down takes its node too when nothing is left. --keep
// on a box whose spaces are empty stops the box AND removes the node dir — an
// empty record is cruft, not history, so it never lingers as a `gone` row.
func TestRmKeepEmptyBoxRemovesNode(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "keep-bbbb", "inst-b") // no space entries → all empty
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-b", Status: "running"}}}

	if err := newReal("", fd, drv).Rm(params.Rm{Node: "keep-bbbb", Keep: true}); err != nil {
		t.Fatalf("rm --keep: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "inst-b" {
		t.Fatalf("--keep must stop the box, downed %v", drv.downs)
	}
	if !rmAllHas(fd, nodeBase+"/keep-bbbb") {
		t.Errorf("--keep on an empty box must remove the node dir (no gone cruft): %v", fd.rmAll)
	}
}

// CONTRACT: -y consents, so the held space is reaped and the node removed.
func TestRmHeldSpaceWithConsentReaps(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "reap-aaaa", "inst-a")
	spaceHeld(fd, "reap-aaaa", "held")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-a", Status: "running"}}}

	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Rm(params.Rm{Node: "reap-aaaa", Yes: true}); err != nil {
			t.Fatalf("rm -y: %v", err)
		}
	})
	if !strings.Contains(out, "removed") {
		t.Errorf("rm -y should reap the held space and remove the node; got:\n%s", out)
	}
	if !rmAllHas(fd, nodeBase+"/reap-aaaa/held") {
		t.Errorf("held-space not reaped with -y: %v", fd.rmAll)
	}
}

// CONTRACT: an EMPTY held space on a box that is NOT live is reaped silently —
// never prompted about, never "kept" — and the node is removed. Nothing is at
// stake (no live box, no held data), so no consent is needed.
func TestRmEmptyHeldSpaceReapsSilently(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "gone-aaaa", "inst-a") // no space entries → all empty
	drv := &fakeDriver{}                   // box already down → nothing live at stake

	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Rm(params.Rm{Node: "gone-aaaa"}); err != nil {
			t.Fatalf("rm: %v", err)
		}
	})
	if strings.Contains(out, "kept") || strings.Contains(out, "holds files") {
		t.Errorf("empty held space must be reaped silently, not prompted/kept; got:\n%s", out)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("a node with only empty spaces should be removed; got:\n%s", out)
	}
}

// CONTRACT: `rm --inactive` sweeps every INACTIVE subtree — regardless of kind —
// and leaves active ones untouched. An empty project marker is reaped; a live
// box's subtree is not stopped or removed.
func TestRmInactiveSweepsOnlyInactive(t *testing.T) {
	fd := baseData()
	seedNode(fd, "proj-x", "project", "") // empty marker → inactive
	seedBoxNode(fd, "live-box", "inst-live")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-live", Status: "running"}}}

	if err := newReal("", fd, drv).Rm(params.Rm{Inactive: true}); err != nil {
		t.Fatalf("rm --inactive: %v", err)
	}
	if !rmAllHas(fd, nodeBase+"/proj-x") {
		t.Errorf("rm --inactive must reap the empty project marker: %v", fd.rmAll)
	}
	if len(drv.downs) != 0 {
		t.Errorf("rm --inactive must not stop a live box: downed %v", drv.downs)
	}
	if rmAllHas(fd, nodeBase+"/live-box") {
		t.Errorf("rm --inactive must not remove an active box's node: %v", fd.rmAll)
	}
}

// CONTRACT: `rm --inactive` is a BATCH: nodes are listed and the drivers
// queried ONCE for the whole sweep, however many inactive roots it reaps —
// never per node. A gone box's instance is never contacted (no down attempt):
// the one drivers' answer already says it is dead.
func TestRmInactiveQueriesDriversOnce(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "gone-aaaa", "inst-a") // empty spaces, no driver holds the instance
	seedBoxNode(fd, "gone-bbbb", "inst-b")
	seedNode(fd, "proj-x", "project", "")
	drv := &fakeDriver{} // the driver reports nothing → everything is inactive

	if err := newReal("", fd, drv).Rm(params.Rm{Inactive: true}); err != nil {
		t.Fatalf("rm --inactive: %v", err)
	}
	if drv.lsCount != 1 {
		t.Errorf("rm --inactive queried the drivers %d times, want 1 for the whole sweep", drv.lsCount)
	}
	if len(drv.downs) != 0 {
		t.Errorf("rm --inactive must not attempt to down gone instances: %v", drv.downs)
	}
	for _, id := range []string{"gone-aaaa", "gone-bbbb", "proj-x"} {
		if !rmAllHas(fd, nodeBase+"/"+id) {
			t.Errorf("%s not reaped by the sweep: %v", id, fd.rmAll)
		}
	}
}

// CONTRACT: `rm` of a box no driver reports skips the down entirely — the one
// drivers query is the whole answer, no second per-instance round-trip.
func TestRmGoneBoxSkipsDownAndSecondDriversQuery(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "gone-aaaa", "inst-a")
	drv := &fakeDriver{} // no driver reports inst-a → already gone

	if err := newReal("", fd, drv).Rm(params.Rm{Node: "gone-aaaa"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if drv.lsCount != 1 {
		t.Errorf("rm of a gone box queried the drivers %d times, want 1", drv.lsCount)
	}
	if len(drv.downs) != 0 {
		t.Errorf("rm of a gone box must not down anything: %v", drv.downs)
	}
	if !rmAllHas(fd, nodeBase+"/gone-aaaa") {
		t.Errorf("gone box's node not removed: %v", fd.rmAll)
	}
}

// CONTRACT: an INCOMPLETE drivers' answer (a driver errored or timed out) is
// not absence. The down is attempted anyway, and its own resolve — a fresh
// query — still stops the box once the transient failure has passed. Skipping
// it would orphan a running box outside dabs's tracking.
func TestRmDownsBoxWhenDriversAnswerIncomplete(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "flaky-aaaa", "inst-a")
	drv := &fakeDriver{
		infos:     []sandbox.Info{{Name: "inst-a", Status: "running"}},
		lsErrOnce: fmt.Errorf("driver down"), // the sweep's one query fails; the next succeeds
	}
	if err := newReal("", fd, drv).Rm(params.Rm{Node: "flaky-aaaa", Yes: true}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "inst-a" {
		t.Fatalf("an unconfirmed box must still be downed (attempted, resolved fresh); downed %v", drv.downs)
	}
}

// seedNode makes fd look as if dabs had provisioned an arbitrary-kind node,
// optionally under a parent, so a cascade (project → workdirs) can be built.
func seedNode(fd *fakeData, id, kind, parent string) {
	if fd.dirs == nil {
		fd.dirs = map[string][]string{}
	}
	fd.dirs[nodeBase] = append(fd.dirs[nodeBase], id)
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	fd.files[nodeBase+"/"+id+"/dabs-node.json"] = []byte(
		`{"id":"` + id + `","kind":"` + kind + `","parent":"` + parent + `","recipe":"r","created":"` + id + `"}`)
}

// CONTRACT: a cascade reap asks ONCE. Non-interactively it prints ONE aggregated
// data summary (not a line per node) and refuses without -y, reaping nothing.
func TestRmCascadeSummarizesDataAndRefusesWithoutYes(t *testing.T) {
	fd := baseData()
	seedNode(fd, "proj", "project", "")
	seedNode(fd, "wd1", "workdir", "proj")
	seedNode(fd, "wd2", "workdir", "proj")
	spaceHeld(fd, "wd1", "held")
	spaceHeld(fd, "wd2", "held")

	var err error
	out := captureStdout(t, func() {
		err = newReal("", fd, &fakeDriver{}).Rm(params.Rm{Node: "proj"})
	})
	if err == nil {
		t.Fatal("non-interactive cascade must refuse (pass -y), got nil")
	}
	if !strings.Contains(out, "2 node(s) hold a held space") {
		t.Errorf("want ONE aggregated held-space line for both holders; got:\n%s", out)
	}
	if len(fd.rmAll) != 0 {
		t.Errorf("a refused cascade reaped something: %v", fd.rmAll)
	}
}

// CONTRACT: the single -y (or one interactive yes) reaps the WHOLE set, held space
// included, with no further per-node questions.
func TestRmCascadeYesReapsWholeSetIncludingHeld(t *testing.T) {
	fd := baseData()
	seedNode(fd, "proj", "project", "")
	seedNode(fd, "wd1", "workdir", "proj")
	seedNode(fd, "wd2", "workdir", "proj")
	spaceHeld(fd, "wd1", "held")
	spaceHeld(fd, "wd2", "held")

	if err := newReal("", fd, &fakeDriver{}).Rm(params.Rm{Node: "proj", Yes: true}); err != nil {
		t.Fatalf("rm -y: %v", err)
	}
	for _, id := range []string{"wd1", "wd2"} {
		if !rmAllHas(fd, nodeBase+"/"+id+"/held") {
			t.Errorf("%s held space not reaped by the batch -y: %v", id, fd.rmAll)
		}
	}
	for _, id := range []string{"proj", "wd1", "wd2"} {
		if !rmAllHas(fd, nodeBase+"/"+id) {
			t.Errorf("%s node dir not removed: %v", id, fd.rmAll)
		}
	}
}

// CONTRACT (node id is the handle): `rm` takes the NODE id — what `ls` shows —
// and stops the box the node records. With -y consenting to stop the live box.
func TestRmResolvesByNodeId(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "mybox-aaaa", "shell-1234") // node id -> its instance
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "shell-1234", Status: "running"}}}
	if err := newReal("", fd, drv).Rm(params.Rm{Node: "mybox-aaaa", Yes: true}); err != nil {
		t.Fatalf("rm by node id: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "shell-1234" {
		t.Fatalf("rm by node id downed %v, want [shell-1234]", drv.downs)
	}
}

func TestRmResolvesByNodeIdPrefix(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "mybox-aaaa", "shell-1234")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "shell-1234", Status: "running"}}}
	if err := newReal("", fd, drv).Rm(params.Rm{Node: "mybox", Yes: true}); err != nil {
		t.Fatalf("rm by node prefix: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "shell-1234" {
		t.Fatalf("rm by node prefix downed %v, want [shell-1234]", drv.downs)
	}
}

// CONTRACT (fallback, what `down` did): a raw box INSTANCE name — not a node-id
// prefix — resolves to that box's node when no node id matched, so `rm
// <instance>` still stops the box. The instance name here is not a prefix of the
// node id, so only the instance-name fallback can find it.
func TestRmResolvesByRawInstanceName(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "mybox-aaaa", "shell-1234")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "shell-1234", Status: "running"}}}
	if err := newReal("", fd, drv).Rm(params.Rm{Node: "shell-1234", Yes: true}); err != nil {
		t.Fatalf("rm by instance name: %v", err)
	}
	if len(drv.downs) != 1 || drv.downs[0] != "shell-1234" {
		t.Fatalf("rm by instance name downed %v, want [shell-1234]", drv.downs)
	}
}

// CONTRACT: an instance-name PREFIX matching more than one box is refused without
// --multiple — the ambiguity guard is the same whether hits came from node ids or
// instance names.
func TestRmInstanceNamePrefixMultiMatchRefuses(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "node-a", "shell-aaaa")
	seedBoxNode(fd, "node-b", "shell-bbbb")
	drv := &fakeDriver{infos: []sandbox.Info{
		{Name: "shell-aaaa", Status: "running"},
		{Name: "shell-bbbb", Status: "running"},
	}}
	err := newReal("", fd, drv).Rm(params.Rm{Node: "shell", Yes: true})
	if err == nil {
		t.Fatal("instance-name prefix matching two boxes must refuse without --multiple")
	}
	if len(drv.downs) != 0 {
		t.Fatalf("must reap NOTHING on refusal, downed %v", drv.downs)
	}
}

// CONTRACT: when `rm -y` keeps a worktree node's volume, the command the "volume
// kept" line suggests — `rm <node> -y --volume` — works: the record is still
// there, the missing checkout is not asked about, the volume reaps, exit 0.
func TestRmVolumeKeptThenSuggestedRerunReaps(t *testing.T) {
	fd := baseData()
	const id = "wt-vol1"
	held := nodeBase + "/" + id + "/held"
	co := held + "/worktree"
	fd.dirs = map[string][]string{nodeBase: {id}, held: {"worktree"}}
	fd.files = map[string][]byte{
		nodeBase + "/" + id + "/dabs-node.json": []byte(
			`{"id":"` + id + `","recipe":"r","created":"t","worktree":{"branch":"dabs/` + id + `","repo":"/repo"}}`),
	}
	fd.exists[held], fd.isDir[held] = true, true
	fd.exists[co], fd.isDir[co] = true, true
	fd.states[co] = wtState{branch: "dabs/" + id}
	spaceHeld(fd, id, "volume")
	r := newReal("", fd, &fakeDriver{})

	out := captureStdout(t, func() {
		if err := r.Rm(params.Rm{Node: id, Yes: true}); err != nil {
			t.Fatalf("rm -y: %v", err)
		}
	})
	if !strings.Contains(out, "volume kept") || !strings.Contains(out, "-y --volume") {
		t.Fatalf("rm -y should keep the volume and suggest the reap command, got:\n%s", out)
	}
	if _, ok := fd.files[nodeBase+"/"+id+"/dabs-node.json"]; !ok {
		t.Fatalf("the node record must survive while its volume is kept")
	}

	// The exact suggested command: the checkout is gone, the record is not.
	out = captureStdout(t, func() {
		if err := r.Rm(params.Rm{Node: id, Yes: true, Volume: true}); err != nil {
			t.Fatalf("rm -y --volume rerun: %v", err)
		}
	})
	if !strings.Contains(out, "removed") {
		t.Fatalf("the rerun should reap the volume and remove the node, got:\n%s", out)
	}
	if !rmAllHas(fd, nodeBase+"/"+id+"/volume") {
		t.Fatalf("the volume dir was not reaped: %v", fd.rmAll)
	}
	if _, ok := fd.files[nodeBase+"/"+id+"/dabs-node.json"]; ok {
		t.Fatalf("the node record should be gone after the rerun")
	}
}

// CONTRACT: an rm plan that cascades past the name says WHY each node is in
// the set — a descendant whose name shares nothing with the prefix is labeled
// as reaped-with-its-parent, never left looking like a name match.
func TestRmDryLabelsDescendantsOfMatch(t *testing.T) {
	fd := baseData()
	fd.dirs = map[string][]string{nodeBase: {"work-aaaa"}}
	fd.files = map[string][]byte{
		nodeBase + "/work-aaaa/dabs-node.json": []byte(`{"id":"work-aaaa","kind":"project","dir":"/cwd","recipe":"r","created":"t"}`),
	}
	seedChildBoxNode(fd, "myscratch", "inst-s", "work-aaaa")
	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Rm(params.Rm{Node: "work", Dry: true, Multiple: true}); err != nil {
			t.Fatalf("rm --dry: %v", err)
		}
	})
	if !strings.Contains(out, "reaped as descendants") || !strings.Contains(out, "myscratch") {
		t.Fatalf("plan must label myscratch as a descendant, got:\n%s", out)
	}
	if !strings.Contains(out, "work-aaaa") {
		t.Fatalf("plan must name the matched node, got:\n%s", out)
	}
}

// CONTRACT: `rm --dry` on a worktree holding unreviewed work points at the
// work — `dabs worktrees diff <name>` — not just at its existence.
func TestRmDryPointsAtWorktreesDiff(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "wt-dirty", wtState{branch: "b", dirty: true})
	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Rm(params.Rm{Node: "wt-dirty", Dry: true}); err != nil {
			t.Fatalf("rm --dry: %v", err)
		}
	})
	if !strings.Contains(out, "worktrees diff wt-dirty") {
		t.Fatalf("dry preview must point at `dabs worktrees diff`, got:\n%s", out)
	}
}

// CONTRACT: --force on a reap that touches no worktree is called out as having
// no effect — it only ever approves discarding unreviewed worktree work.
func TestRmForceOnNonWorktreeSaysNoEffect(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "boxy", "inst-b")
	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Rm(params.Rm{Node: "boxy", Yes: true, Force: true}); err != nil {
			t.Fatalf("rm --force: %v", err)
		}
	})
	if !strings.Contains(out, "--force had no effect") {
		t.Fatalf("want a no-effect note for --force, got:\n%s", out)
	}
}

// CONTRACT: a `--clean-worktrees` sweep over zero worktrees says so instead of
// printing nothing.
func TestRmCleanWorktreesNothingToCleanSaysSo(t *testing.T) {
	out := captureStdout(t, func() {
		if err := newReal("", baseData(), &fakeDriver{}).Rm(params.Rm{CleanWorktrees: true, Dry: true}); err != nil {
			t.Fatalf("rm --clean-worktrees --dry: %v", err)
		}
	})
	if !strings.Contains(out, "no worktrees to clean") {
		t.Fatalf("want an explicit nothing-to-clean line, got %q", out)
	}
}
