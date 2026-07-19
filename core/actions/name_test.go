package actions_test

// Tests for --name (a chosen node id) and `dabs cd`:
//   - the name IS the leaf node's id: a named box boot writes its node as <name>;
//   - unique: an ACTIVE holder refuses the claim before anything is provisioned;
//   - an INACTIVE holder (an empty marker) is reaped on the fly and the name reused;
//   - an unverifiable holder (a driver did not answer) refuses — silence is not
//     proof its box is gone;
//   - a malformed name is rejected up front;
//   - cd prints the node's directory, bare, resolving names/ids/instances and
//     refusing ambiguity.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// keep: true — a non-keep boot FULLY reaps its node after the command (#59),
// so pins about the node's record need the box kept.
const namedBootRecipe = `default: base
recipes:
  base:
    image: { dockerfile: Dockerfile, context: . }
    command: [x]
    keep: true
    sources:
      - mount: /data
        path: /work
`

func bootNamed(t *testing.T, fd *fakeData, drv *fakeDriver, nodeName string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = newReal(namedBootRecipe, fd, drv).Recipe(params.Recipe{Name: "base", NodeName: nodeName})
	})
	return out, err
}

func namedBootData(t *testing.T) *fakeData {
	t.Helper()
	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{"/cwd/Dockerfile": []byte("FROM alpine\n")}
	return fd
}

// CONTRACT: --name is the box node's id — the one handle every verb resolves
// and every message shows.
func TestNamedBootUsesNameAsNodeID(t *testing.T) {
	fd := namedBootData(t)
	drv := &fakeDriver{}
	if _, err := bootNamed(t, fd, drv, "feature-x"); err != nil {
		t.Fatalf("named boot: %v", err)
	}
	rec, ok := fd.files[nodeBase+"/feature-x/dabs-node.json"]
	if !ok {
		t.Fatalf("no node record at the chosen name; files: %v", keysOf(fd.files))
	}
	if !strings.Contains(string(rec), `"box"`) {
		t.Fatalf("named node is not the box (the boot's leaf): %s", rec)
	}
}

// CONTRACT: a name held by an ACTIVE node refuses the boot before anything is
// provisioned — nothing built, nothing up, nothing reaped.
func TestNamedBootRefusesActiveHolder(t *testing.T) {
	fd := namedBootData(t)
	seedBoxNode(fd, "feature-x", "inst-live")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-live", Status: "running"}}}
	_, err := bootNamed(t, fd, drv, "feature-x")
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("want an active-holder refusal, got %v", err)
	}
	if len(drv.ups) != 0 || len(drv.downs) != 0 {
		t.Fatalf("a refused claim must touch nothing: ups=%v downs=%v", drv.ups, drv.downs)
	}
}

// CONTRACT: a name held by an INACTIVE node — an empty marker `ls` hides — is
// garbage-collected on the fly, and the boot proceeds under that name.
// Otherwise every name would be single-use.
func TestNamedBootReapsInactiveHolder(t *testing.T) {
	fd := namedBootData(t)
	seedNode(fd, "feature-x", "project", "") // empty marker: inactive
	drv := &fakeDriver{}
	out, err := bootNamed(t, fd, drv, "feature-x")
	if err != nil {
		t.Fatalf("named boot over an inactive holder: %v", err)
	}
	if !rmAllHas(fd, nodeBase+"/feature-x") {
		t.Fatalf("inactive holder not reaped: %v", fd.rmAll)
	}
	if !strings.Contains(out, "inactive") {
		t.Fatalf("the on-the-fly reap must say so; got:\n%s", out)
	}
	rec, ok := fd.files[nodeBase+"/feature-x/dabs-node.json"]
	if !ok || !strings.Contains(string(rec), `"box"`) {
		t.Fatalf("name not reused by the new boot's box: %s", rec)
	}
}

// CONTRACT: a holder that cannot be VERIFIED inactive (a driver did not answer)
// refuses the claim — reaping on silence could take a live box's name.
func TestNamedBootRefusesUnverifiableHolder(t *testing.T) {
	fd := namedBootData(t)
	seedBoxNode(fd, "feature-x", "inst-a")
	drv := &fakeDriver{lsErrOnce: fmt.Errorf("driver down")}
	_, err := bootNamed(t, fd, drv, "feature-x")
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("want an unverifiable refusal, got %v", err)
	}
	if len(fd.rmAll) != 0 {
		t.Fatalf("nothing may be reaped on an unverified claim: %v", fd.rmAll)
	}
}

// CONTRACT: a malformed name is rejected before anything happens.
func TestNamedBootRejectsMalformedName(t *testing.T) {
	fd := namedBootData(t)
	drv := &fakeDriver{}
	for _, bad := range []string{"-leading-dash", "has space", "sl/ash", strings.Repeat("x", 70),
		"a..b", "trailing.", "wt.lock"} { // git refuses these as branch names — refused up front
		if _, err := bootNamed(t, fd, drv, bad); err == nil {
			t.Errorf("name %q accepted, want rejection", bad)
		}
	}
	if len(drv.ups) != 0 {
		t.Fatalf("a rejected name must boot nothing: %v", drv.ups)
	}
}

// CONTRACT: a name equal to some box's INSTANCE name is refused — one handle
// must never mean two boxes (exec/rm/cd must agree what it names).
func TestNamedBootRefusesInstanceCollision(t *testing.T) {
	fd := namedBootData(t)
	seedBoxNode(fd, "other-box", "sh-4f2a91bc03de")
	drv := &fakeDriver{}
	_, err := bootNamed(t, fd, drv, "sh-4f2a91bc03de")
	if err == nil || !strings.Contains(err.Error(), "instance") {
		t.Fatalf("want an instance-collision refusal, got %v", err)
	}
}

// CONTRACT: the claim runs LAST among the refusals. A boot refused for another
// reason — here a boxless recipe with several places — must leave the name's
// inactive holder untouched.
func TestRefusedBootLeavesInactiveHolderAlone(t *testing.T) {
	fd := baseData()
	fd.exists["/d1"], fd.exists["/d2"] = true, true
	seedNode(fd, "probe", "project", "") // inactive holder of the name
	y := `recipes:
  twoplace:
    sources:
      - copy: /d1
        path: /a
      - copy: /d2
        path: /b
`
	drv := &fakeDriver{}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "twoplace", NodeName: "probe"})
	if err == nil || !strings.Contains(err.Error(), "ONE node") {
		t.Fatalf("want the multi-place refusal, got %v", err)
	}
	if rmAllHas(fd, nodeBase+"/probe") {
		t.Fatalf("a refused boot reaped the name's holder: %v", fd.rmAll)
	}
}

// CONTRACT: the claim RESERVES the name with an exclusive dir create (the mint
// lock). A leftover dir with no record and no fresh claim marker — an earlier
// boot that died there, long enough ago — is reclaimed, not a permanent squat.
func TestNamedBootReclaimsRecordlessDir(t *testing.T) {
	fd := namedBootData(t)
	fd.made = append(fd.made, nodeBase+"/feature-x") // dir exists, no record, no marker
	drv := &fakeDriver{}
	if _, err := bootNamed(t, fd, drv, "feature-x"); err != nil {
		t.Fatalf("recordless dir must be reclaimed, got %v", err)
	}
	if _, ok := fd.files[nodeBase+"/feature-x/dabs-node.json"]; !ok {
		t.Fatalf("name not claimed after reclaim; files: %v", keysOf(fd.files))
	}
}

// CONTRACT: the reservation is a LOCK, not a suggestion. A dir holding a FRESH
// claim marker is another boot mid-claim (the window between its reserve and
// its record) — refused, never deleted from under it. A STALE marker is a dead
// boot's litter and is reclaimed. A record that landed just after our listing
// refuses too.
func TestNamedBootRespectsConcurrentClaim(t *testing.T) {
	fresh := namedBootData(t)
	fresh.made = append(fresh.made, nodeBase+"/feature-x")
	fresh.files[nodeBase+"/feature-x/dabs-claim"] = []byte(time.Now().UTC().Format(time.RFC3339) + "\n")
	if _, err := bootNamed(t, fresh, &fakeDriver{}, "feature-x"); err == nil || !strings.Contains(err.Error(), "claiming it right now") {
		t.Fatalf("a fresh concurrent claim must refuse, got %v", err)
	}
	if rmAllHas(fresh, nodeBase+"/feature-x") {
		t.Fatalf("a live claim was deleted from under its boot: %v", fresh.rmAll)
	}

	stale := namedBootData(t)
	stale.made = append(stale.made, nodeBase+"/feature-x")
	stale.files[nodeBase+"/feature-x/dabs-claim"] = []byte(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + "\n")
	if _, err := bootNamed(t, stale, &fakeDriver{}, "feature-x"); err != nil {
		t.Fatalf("a stale claim is litter and must be reclaimed, got %v", err)
	}

	// The record landed between our node listing and the reserve: a winner.
	won := namedBootData(t)
	won.made = append(won.made, nodeBase+"/feature-x")
	won.files[nodeBase+"/feature-x/dabs-node.json"] = []byte(`{"id":"feature-x","kind":"box","recipe":"r","created":"t"}`)
	if _, err := bootNamed(t, won, &fakeDriver{}, "feature-x"); err == nil || !strings.Contains(err.Error(), "just claimed it") {
		t.Fatalf("a landed concurrent record must refuse, got %v", err)
	}
}

// CONTRACT: every refusal that does not need the name comes BEFORE the claim —
// a declined confirm, a missing image, a boxless recipe with nothing to name —
// so a refused boot leaves the name's inactive holder untouched.
func TestRefusalsBeforeClaimLeaveHolderAlone(t *testing.T) {
	// Declined confirm (an appended command always confirms).
	fd := namedBootData(t)
	seedNode(fd, "probe", "project", "")
	var err error
	out := captureStdout(t, func() {
		err = newReal(namedBootRecipe, fd, &fakeDriver{}).WithConfirm(func(string) bool { return false }).
			Recipe(params.Recipe{Args: []string{"base", "echo"}, NodeName: "probe"})
	})
	if err == nil {
		t.Fatal("declined confirm must refuse the boot")
	}
	if rmAllHas(fd, nodeBase+"/probe") || strings.Contains(out, "reaping") {
		t.Fatalf("a declined confirm reaped the name's holder:\n%s\nrmAll=%v", out, fd.rmAll)
	}

	// Missing image (bare name, not built, not bundled).
	fd2 := baseData()
	fd2.exists["/data"] = true
	seedNode(fd2, "probe", "project", "")
	y := "recipes:\n  m:\n    image: img\n    command: [x]\n    sources:\n      - mount: /data\n        path: /work\n"
	if err := newReal(y, fd2, &fakeDriver{}).Recipe(params.Recipe{Name: "m", NodeName: "probe"}); err == nil {
		t.Fatal("missing image must refuse the boot")
	}
	if rmAllHas(fd2, nodeBase+"/probe") {
		t.Fatalf("a missing-image refusal reaped the name's holder: %v", fd2.rmAll)
	}

	// A boxless recipe with no place to name.
	fd3 := baseData()
	fd3.exists["/data"] = true
	seedNode(fd3, "probe", "project", "")
	y3 := "recipes:\n  m:\n    sources:\n      - mount: /data\n        path: /work\n"
	if err := newReal(y3, fd3, &fakeDriver{}).Recipe(params.Recipe{Name: "m", NodeName: "probe"}); err == nil {
		t.Fatal("a boxless mount-only recipe with --name must refuse")
	}
	if rmAllHas(fd3, nodeBase+"/probe") {
		t.Fatalf("a nothing-to-name refusal reaped the name's holder: %v", fd3.rmAll)
	}
}

// CONTRACT: `dabs rm <name>` clears an UNFINISHED claim — the marker-only dir
// a died `--name` boot leaves — which no listing shows and no other verb can
// reach. That is the escape hatch the claim's own refusal message offers, so
// it must actually work. A claim dir holding real files (a died boot's
// provisioning) needs -y like any held data.
func TestRmClearsUnfinishedClaim(t *testing.T) {
	fd := baseData()
	fd.made = append(fd.made, nodeBase+"/stuck")
	fd.dirs = map[string][]string{nodeBase + "/stuck": {"dabs-claim"}}
	fd.files = map[string][]byte{nodeBase + "/stuck/dabs-claim": []byte(time.Now().UTC().Format(time.RFC3339) + "\n")}
	fd.exists[nodeBase+"/stuck"] = true
	fd.exists[nodeBase+"/stuck/dabs-claim"] = true
	if err := newReal("", fd, &fakeDriver{}).Rm(params.Rm{Node: "stuck"}); err != nil {
		t.Fatalf("rm of an unfinished claim: %v", err)
	}
	if !rmAllHas(fd, nodeBase+"/stuck") {
		t.Fatalf("unfinished claim not reaped: %v", fd.rmAll)
	}

	// Holding a real file beyond the marker: -y is the consent.
	fd2 := baseData()
	fd2.made = append(fd2.made, nodeBase+"/stuck")
	fd2.dirs = map[string][]string{
		nodeBase + "/stuck":      {"dabs-claim", "held"},
		nodeBase + "/stuck/held": {"work.txt"},
	}
	fd2.files = map[string][]byte{
		nodeBase + "/stuck/dabs-claim":    []byte(time.Now().UTC().Format(time.RFC3339) + "\n"),
		nodeBase + "/stuck/held/work.txt": []byte("x"),
	}
	fd2.exists[nodeBase+"/stuck"] = true
	fd2.exists[nodeBase+"/stuck/dabs-claim"] = true
	if err := newReal("", fd2, &fakeDriver{}).Rm(params.Rm{Node: "stuck"}); err == nil {
		t.Fatal("a claim dir holding files must need -y")
	}
	if err := newReal("", fd2, &fakeDriver{}).Rm(params.Rm{Node: "stuck", Yes: true}); err != nil {
		t.Fatalf("rm -y of a file-holding claim: %v", err)
	}
	if !rmAllHas(fd2, nodeBase+"/stuck") {
		t.Fatalf("file-holding claim not reaped with -y: %v", fd2.rmAll)
	}
}

// CONTRACT: `dabs cd` prints a node's WORKING PLACE — a project's source repo
// Dir — bare and absolute, resolving a name/id (and a box instance as fallback)
// git-style, refusing ambiguity. A box has no working place of its own, so it
// falls back to its node dir under ~/.dabs/nodes/<id>.
func TestCdPrintsNodeDirectory(t *testing.T) {
	fd := baseData()
	// A project node with a Dir.
	fd.dirs = map[string][]string{nodeBase: {"proj-x"}}
	fd.files = map[string][]byte{
		nodeBase + "/proj-x/dabs-node.json": []byte(`{"id":"proj-x","kind":"project","dir":"/repo/x","recipe":"r","created":"t"}`),
	}
	seedBoxNode(fd, "boxy", "inst-b")
	drv := &fakeDriver{}
	r := newReal("", fd, drv)

	out := captureStdout(t, func() {
		if err := r.Cd(params.Cd{Node: "proj-x"}); err != nil {
			t.Fatalf("cd project: %v", err)
		}
	})
	if strings.TrimSpace(out) != "/repo/x" {
		t.Fatalf("cd project printed %q, want the source repo /repo/x", out)
	}

	out = captureStdout(t, func() {
		if err := r.Cd(params.Cd{Node: "inst-b"}); err != nil { // instance fallback
			t.Fatalf("cd by instance: %v", err)
		}
	})
	if strings.TrimSpace(out) != nodeBase+"/boxy" {
		t.Fatalf("cd box printed %q, want %s", out, nodeBase+"/boxy")
	}

	if err := r.Cd(params.Cd{Node: "ghost"}); err == nil {
		t.Fatal("cd of a missing node must fail")
	}
	seedBoxNode(fd, "boxy2", "inst-c")
	if err := r.Cd(params.Cd{Node: "box"}); err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Fatalf("ambiguous cd must refuse naming both, got %v", err)
	}
}
