package actions_test

// Tests for instance-name resolution shared by every verb that takes a name.
// The footgun: an empty/blank name is a prefix of EVERY instance, so a naive
// prefix match "matches" the whole fleet. Contract (AGENTS.md): an empty/blank
// name matches NOTHING — never "all" — on every verb, not just down.

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
	"testing/fstest"
)

// fleet wires a Real over an ordered set of named drivers, so a resolution test
// can place a box on one target and prove which drivers get contacted.
func fleet(order []string, drivers map[string]sandbox.Driver) actions.Real {
	return actions.New(drivers, order, fstest.MapFS{}, baseData())
}

// CONTRACT: an exact name held by a LOCAL driver resolves without ever
// contacting a server — the server's Ls panics if reached, so a green test
// proves no remote round-trip.
func TestExactLocalMatchNeverContactsServer(t *testing.T) {
	local := &fakeDriver{infos: []sandbox.Info{{Name: "box-abc123", Driver: "apple"}}}
	server := &fakeDriver{kind: "ssh", lsPanic: true}
	drv := fleet([]string{"local", "homelab"}, map[string]sandbox.Driver{"local": local, "homelab": server})

	if err := drv.Exec(params.Exec{Instance: "box-abc123", Cmd: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(local.runs) != 1 {
		t.Fatalf("want the local box run, got runs=%v", local.runs)
	}
}

// CONTRACT: the same short-circuit holds when the box lives on a SECOND local
// driver (docker-kind, non-server) — a docker box resolves without touching the
// server, which is the ~8x slowdown this fixes.
func TestExactMatchOnSecondLocalDriverNeverContactsServer(t *testing.T) {
	apple := &fakeDriver{kind: "apple"}
	docker := &fakeDriver{kind: "docker", infos: []sandbox.Info{{Name: "box-docker01", Driver: "docker"}}}
	server := &fakeDriver{kind: "ssh", lsPanic: true}
	drv := fleet(
		[]string{"local", "docker", "homelab"},
		map[string]sandbox.Driver{"local": apple, "docker": docker, "homelab": server},
	)

	if err := drv.Exec(params.Exec{Instance: "box-docker01", Cmd: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(docker.runs) != 1 {
		t.Fatalf("want the docker box run, got runs=%v", docker.runs)
	}
}

// CONTRACT: a name found ONLY on a server still resolves — servers are consulted
// when no local driver holds a match, so the fast path never hides remote boxes.
func TestServerOnlyNameStillResolves(t *testing.T) {
	local := &fakeDriver{} // no boxes locally
	lsHit := false
	server := &fakeDriver{kind: "ssh", lsCall: &lsHit, infos: []sandbox.Info{{Name: "remote-xyz", Driver: "ssh"}}}
	drv := fleet([]string{"local", "homelab"}, map[string]sandbox.Driver{"local": local, "homelab": server})

	if err := drv.Exec(params.Exec{Instance: "remote-xyz", Cmd: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !lsHit {
		t.Fatal("server was never contacted, but the box lives only there")
	}
	if len(server.runs) != 1 {
		t.Fatalf("want the remote box run, got runs=%v", server.runs)
	}
}

// twoBoxes is a driver holding a couple of running boxes plus an unrelated one,
// for name-resolution safety checks.
func twoBoxes() *fakeDriver {
	return &fakeDriver{infos: []sandbox.Info{
		{Name: "demo-a1b2", Status: "running", Driver: "fake"},
		{Name: "demo-c3d4", Status: "running", Driver: "fake"},
		{Name: "other-e5f6", Status: "running", Driver: "fake"},
	}}
}

// CONTRACT: a prefix that lands on TWO distinct boxes across the two handle
// namespaces — one box by its node id, a DIFFERENT box by its instance name —
// is ambiguous for exec, not a silent pick of one. Both handles must be checked;
// matching one namespace to the exclusion of the other is the bug.
func TestExecCrossNamespacePrefixIsAmbiguous(t *testing.T) {
	fd := baseData()
	// Box A: its NODE id starts with "abc"; its instance does not.
	seedBoxNode(fd, "abc-node-1", "shell-1111")
	// Box B: its INSTANCE name starts with "abc"; its node id does not.
	seedBoxNode(fd, "zzz-node-2", "abc-box-2")
	drv := &fakeDriver{infos: []sandbox.Info{
		{Name: "shell-1111", Status: "running"},
		{Name: "abc-box-2", Status: "running"},
	}}
	err := newReal("", fd, drv).Exec(params.Exec{Instance: "abc", Cmd: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("cross-namespace prefix must be ambiguous, got %v", err)
	}
	if len(drv.execs) != 0 || len(drv.runs) != 0 {
		t.Fatalf("must touch NO box on ambiguity, execs=%v runs=%v", drv.execs, drv.runs)
	}
}

// CONTRACT: a blank name on run/exec/rm is refused, reaches no driver, and is
// never reported as "ambiguous" — blank matches nothing, not everything.
func TestBlankInstanceNameMatchesNothingOnEveryVerb(t *testing.T) {
	verbs := map[string]func(actions.Real, string) error{
		"exec-shell": func(a actions.Real, n string) error {
			return a.Exec(params.Exec{Instance: n, Cmd: []string{"echo"}, Shell: true})
		},
		"exec": func(a actions.Real, n string) error { return a.Exec(params.Exec{Instance: n, Cmd: []string{"echo"}}) },
		"rm":   func(a actions.Real, n string) error { return a.Rm(params.Rm{Node: n}) },
	}
	for _, name := range []string{"", "   ", "\t"} {
		for label, call := range verbs {
			drv := twoBoxes()
			err := call(newReal("", baseData(), drv), name)
			if err == nil {
				t.Fatalf("%s with name %q: want an error, got nil", label, name)
			}
			if strings.Contains(err.Error(), "ambiguous") {
				t.Errorf("%s with name %q: blank must match NOTHING, got %v", label, name, err)
			}
			if len(drv.runs) != 0 || len(drv.downs) != 0 || len(drv.execs) != 0 {
				t.Errorf("%s with name %q: touched boxes (runs=%v downs=%v)", label, name, drv.runs, drv.downs)
			}
		}
	}
}
