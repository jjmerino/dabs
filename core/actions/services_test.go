package actions_test

// Contract tests for the publishing grant: which boxes get a door, what a box
// without the grant gets, and what dabs records so the door's relay can be
// reaped with the box.

import (
	"errors"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/door"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// errNoRelay is what a host that cannot start a relay says.
var errNoRelay = errors.New("no relay could be started here")

// doorOf returns the box's door socket among a boot's sockets, or "" when the
// box was given none.
func doorOf(up sandbox.Spec) string {
	for _, s := range up.Sockets {
		if s.Path == door.BoxPath {
			return s.Host
		}
	}
	return ""
}

// CONTRACT: publishing is granted, never ambient. A recipe that does not ask
// for it boots a box with NO door — nothing attached, no relay started, no
// registry directory made.
func TestABoxWithoutTheGrantGetsNoDoor(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [run, it]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.exists["/data"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	up := onlyUp(t, drv)
	if host := doorOf(up); host != "" {
		t.Errorf("an ungranted box was given a door at %s", host)
	}
	if len(fd.relays) != 0 {
		t.Errorf("an ungranted box started relays %+v", fd.relays)
	}
	for _, d := range fd.mkdirs {
		if strings.HasSuffix(d, "/services") {
			t.Errorf("an ungranted box was given a registry at %s", d)
		}
	}
	if len(up.Mounts) != 1 || up.Mounts[0].Path != "/work" {
		t.Errorf("mounts = %+v, want just the recipe's own source", up.Mounts)
	}
}

// CONTRACT: a recipe that says `publish: true` boots a box whose door is a
// dabs-owned socket in the box node's OWN tmp space, answered by a relay
// publishing into that node's registry — so a service cannot outlive its box —
// and the relay's pid is recorded on the node for whoever reaps the box.
func TestAGrantedBoxGetsADoorAnsweredByItsOwnRelay(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [sh]
    publish: true
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	// Booted and left up: a box torn down at the end of its command takes its
	// node record with it, and the record is what this reads.
	captureStdout(t, func() {
		if err := newReal(y, fd, drv).Recipe(params.Recipe{Args: []string{"m"}, NoCommand: true, NodeName: "mybox"}); err != nil {
			t.Fatalf("Recipe: %v", err)
		}
	})
	up := onlyUp(t, drv)
	wantDoor := "/home/t/.dabs/nodes/mybox/tmp/door.sock"
	if host := doorOf(up); host != wantDoor {
		t.Fatalf("the box's door is %q, want the box node's own %q", host, wantDoor)
	}
	if len(fd.relays) != 1 {
		t.Fatalf("relays started = %+v, want exactly one", fd.relays)
	}
	got := fd.relays[0]
	if got.door != wantDoor {
		t.Errorf("the relay answers %q, want the box's door %q", got.door, wantDoor)
	}
	if want := "/home/t/.dabs/nodes/mybox/tmp/services"; got.dir != want {
		t.Errorf("the relay publishes into %q, want the box node's tmp space %q", got.dir, want)
	}
	node := string(fd.files["/home/t/.dabs/nodes/mybox/dabs-node.json"])
	if !strings.Contains(strings.ReplaceAll(node, " ", ""), `"relayPid":4001`) {
		t.Errorf("the box node does not record the relay's pid, so nothing can reap it:\n%s", node)
	}
}

// CONTRACT: bringing a box down reaps the relay answering its door, by the pid
// its node records — a recorded pid nothing ever kills leaves a host process
// (and a live socket) behind every reaped box.
func TestReapingABoxReapsTheRelayAnsweringItsDoor(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [sh]
    publish: true
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}, infos: []sandbox.Info{{Name: "img-inst", Driver: "fake"}}}
	r := newReal(y, fd, drv)
	captureStdout(t, func() {
		if err := r.Recipe(params.Recipe{Args: []string{"m"}, NoCommand: true, NodeName: "mybox"}); err != nil {
			t.Fatalf("Recipe: %v", err)
		}
	})
	if len(fd.relays) != 1 {
		t.Fatalf("relays started = %+v, want exactly one", fd.relays)
	}
	if len(fd.reaped) != 0 {
		t.Fatalf("a live box's relay was reaped: %v", fd.reaped)
	}
	captureStdout(t, func() {
		if err := r.Rm(params.Rm{Node: "mybox", Yes: true}); err != nil {
			t.Fatalf("Rm: %v", err)
		}
	})
	if len(fd.reaped) != 1 || fd.reaped[0] != 4001 {
		t.Errorf("reaped pids = %v, want the relay pid the node recorded (4001)", fd.reaped)
	}
}

// CONTRACT: a relay that cannot be started fails the boot. A box booted with a
// door onto nothing would take every publish and answer none of them.
func TestABoxIsNotBootedWithADoorNothingAnswers(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [sh]
    publish: true
`
	fd := baseData()
	fd.relayErr = errNoRelay
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil {
		t.Fatal("the boot succeeded with no relay behind the door")
	}
	if !strings.Contains(err.Error(), errNoRelay.Error()) {
		t.Errorf("the boot failed with %v, want it to name what the relay said", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("a box was booted anyway: %+v", drv.ups)
	}
}

// CONTRACT: a grant dabs cannot realize refuses BY NAME, before anything is
// provisioned — a place has no box to open a door into, and a box on another
// machine has no path to a relay on this one.
func TestAGrantThatCannotBeRealizedIsRefusedByName(t *testing.T) {
	for _, tc := range []struct{ what, yaml, kind, want string }{
		{
			what: "a recipe with no image",
			yaml: `recipes:
  m:
    command: [sh]
    publish: true
    sources:
      - copy: /data
        path: /work
`,
			want: "only makes places",
		},
		{
			what: "a recipe on a server",
			yaml: `recipes:
  m:
    image: img
    command: [sh]
    publish: true
`,
			kind: "ssh",
			want: "another machine",
		},
	} {
		fd := baseData()
		fd.exists["/data"] = true
		fd.isDir["/data"] = true
		drv := &fakeDriver{built: map[string]bool{"img": true}, kind: tc.kind}
		err := newReal(tc.yaml, fd, drv).Recipe(params.Recipe{Name: "m"})
		if err == nil {
			t.Errorf("%s: the boot was allowed", tc.what)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refused with %q, want it to say %q", tc.what, err, tc.want)
		}
		if len(fd.relays) != 0 {
			t.Errorf("%s: a relay was started anyway: %+v", tc.what, fd.relays)
		}
	}
}

// CONTRACT: the door is one of dabs's own box paths, so a recipe socket may not
// land on it — a recipe masking the door would take publishing away from the
// box with nothing failing until a program in it reached for the door.
func TestARecipeSocketMayNotLandOnTheDoor(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [sh]
    sockets:
      - socket: /run/agent.sock
        path: ` + door.BoxPath + `
`
	fd := baseData()
	listenSocket(fd, "/run/agent.sock")
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil {
		t.Fatal("a recipe socket was allowed to land on the door")
	}
	if !strings.Contains(err.Error(), door.BoxPath) {
		t.Errorf("refused with %q, want it to name the door path", err)
	}
}
