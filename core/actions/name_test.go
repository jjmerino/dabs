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

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

const namedBootRecipe = `default: base
recipes:
  base:
    image: { dockerfile: Dockerfile, context: . }
    command: [x]
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
	for _, bad := range []string{"-leading-dash", "has space", "sl/ash", strings.Repeat("x", 70)} {
		if _, err := bootNamed(t, fd, drv, bad); err == nil {
			t.Errorf("name %q accepted, want rejection", bad)
		}
	}
	if len(drv.ups) != 0 {
		t.Fatalf("a rejected name must boot nothing: %v", drv.ups)
	}
}

// CONTRACT: `dabs cd` prints a node's directory — bare, absolute — resolving a
// name/id (and a box instance as fallback) git-style, refusing ambiguity.
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
		t.Fatalf("cd project printed %q, want /repo/x", out)
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
