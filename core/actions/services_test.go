package actions_test

// Contract tests for the services seam: what every box gets, whatever its
// recipe asked for.

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// CONTRACT: every box is given the services directory — a program inside can
// publish without its recipe declaring anything — and that directory is the box
// node's OWN tmp space, so what it publishes dies with the box.
func TestEveryBoxMountsItsOwnServicesDir(t *testing.T) {
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
	host := ""
	for _, m := range up.Mounts {
		if m.Path == forwarder.ServicesDir {
			host = m.Host
		}
	}
	if host == "" {
		t.Fatalf("no mount at %s; mounts = %+v", forwarder.ServicesDir, up.Mounts)
	}
	if want := "/home/t/.dabs/nodes/mybox/tmp/services"; host != want {
		t.Errorf("services dir = %q, want the box node's tmp space %q", host, want)
	}
	made := false
	for _, d := range fd.mkdirs {
		if d == host {
			made = true
		}
	}
	if !made {
		t.Errorf("services dir %s was not created; created = %v", host, fd.mkdirs)
	}
}

// CONTRACT: a recipe that declares nothing still gets the services directory —
// the box side of services is not opt-in.
func TestABareRecipeStillGetsTheServicesDir(t *testing.T) {
	y := `recipes:
  bare:
    image: img
    command: [sh]
`
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, baseData(), drv).Recipe(params.Recipe{Name: "bare"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	up := onlyUp(t, drv)
	if len(up.Mounts) != 1 || up.Mounts[0].Path != forwarder.ServicesDir {
		t.Fatalf("mounts = %+v, want just the services dir", up.Mounts)
	}
	if !strings.HasSuffix(up.Mounts[0].Host, "/tmp/services") {
		t.Errorf("services dir = %q, want it inside the box node's tmp space", up.Mounts[0].Host)
	}
}
