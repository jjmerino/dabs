package actions_test

// Component tests for the LIBRARY entry point: booting a box from a recipe
// VALUE. Driven through the public API with the driver and data seams faked, so
// the refusals are pinned without a real box.

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
)

// CONTRACT: a recipe that exists only as a Go value boots, with no registry
// entry and no dabs.yaml behind it, and its identity comes back to the caller.
func TestBootFromValueNeedsNoRegistry(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	box, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name: "inmem",
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Env:     map[string]string{"A": "b"},
		},
	})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if box.ID == "" || box.Instance == "" {
		t.Fatalf("Boot must return an identity, got %+v", box)
	}
	spec := onlyUp(t, drv)
	if spec.Env["A"] != "b" {
		t.Errorf("the value's env must reach the driver, got %v", spec.Env)
	}
}

// CONTRACT: the library entry point boots a recipe VALUE, which never passed
// through a recipes file — so it meets the socket gate too. A caller handing
// Boot a socket box path that escapes with `..`, or one that lands on a path
// dabs binds itself, is refused before any box comes up.
func TestBootRefusesBadSocketBoxPath(t *testing.T) {
	for _, boxPath := range []string{"/run/dabs/../../etc/passwd", "/run/dabs/door.sock"} {
		t.Run(boxPath, func(t *testing.T) {
			fd := baseData()
			listenSocket(fd, "/run/one.sock")
			drv := &fakeDriver{built: map[string]bool{"img": true}}
			_, err := newReal("", fd, drv).Boot(actions.BootSpec{
				Name: "inmem",
				Recipe: recipe.Recipe{
					Image:   recipe.ImageRef{Name: "img"},
					Command: []string{"sh"},
					Sockets: []recipe.Socket{{Socket: "/run/one.sock", Path: boxPath}},
				},
			})
			if err == nil {
				t.Fatalf("Boot brought a box up with socket box path %q", boxPath)
			}
			if len(drv.ups) != 0 {
				t.Errorf("driver was handed %q: %v", boxPath, drv.ups)
			}
		})
	}
}

// CONTRACT: Boot answers for the server refusal on its own. It shares a call site
// with the `--no-command` path, so one test cannot speak for both entry points —
// a caller handing Boot a recipe with sockets and a server target is refused
// before the remote driver is asked for anything.
func TestBootRefusesSocketsOnAServer(t *testing.T) {
	fd := baseData()
	listenSocket(fd, "/run/one.sock")
	remote := &fakeDriver{built: map[string]bool{"img": true}, kind: "ssh"}
	real := actions.New(map[string]sandbox.Driver{"local": &fakeDriver{}, "builder": remote},
		[]string{"local", "builder"}, fstest.MapFS{}, fd)
	_, err := real.Boot(actions.BootSpec{
		Name: "inmem",
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Target:  "builder",
			Sockets: []recipe.Socket{{Socket: "/run/one.sock", Path: "/run/dabs/one.sock"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "another machine") {
		t.Fatalf("Boot put sockets on a server: %v", err)
	}
	if len(remote.ups) != 0 {
		t.Errorf("the server driver was handed a box: %v", remote.ups)
	}
}

// CONTRACT: Boot runs NOTHING. It is the `--no-command` form: the box comes up
// and the recipe's command is left for the caller to run through Exec, so a
// recipe carrying a command must still reach the driver as a bare boot.
func TestBootRunsNoCommand(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if _, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name:   "inmem",
		Recipe: recipe.Recipe{Image: recipe.ImageRef{Name: "img"}, Command: []string{"sh", "-c", "server"}},
	}); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if len(drv.runs) != 0 {
		t.Errorf("Boot must not run the recipe's command, got %v", drv.runs)
	}
	if len(drv.detached) != 0 {
		t.Errorf("Boot must not start the recipe's command detached, got %v", drv.detached)
	}
}

// undetachableDriver forwards the whole Driver surface and nothing else, so it
// is not a Detacher: neither asking the capability nor asserting the type finds
// one here.
type undetachableDriver struct{ sandbox.Driver }

// CONTRACT: Boot does not need a driver that can hold a detached command. It
// starts no command, so the detach capability never enters into it — a driver
// that lacks it entirely, and one that answers that it cannot detach, both boot.
func TestBootNeedsNoDetacher(t *testing.T) {
	t.Run("driver is not a Detacher at all", func(t *testing.T) {
		fd := baseData()
		drv := &fakeDriver{built: map[string]bool{"img": true}}
		box, err := newReal("", fd, undetachableDriver{drv}).Boot(actions.BootSpec{
			Name:   "inmem",
			Recipe: recipe.Recipe{Image: recipe.ImageRef{Name: "img"}, Command: []string{"sh"}},
		})
		if err != nil {
			t.Fatalf("Boot: %v", err)
		}
		if box.ID == "" || box.Instance == "" {
			t.Fatalf("Boot must return an identity, got %+v", box)
		}
	})
	t.Run("driver answers that it cannot detach", func(t *testing.T) {
		fd := baseData()
		drv := &fakeDriver{built: map[string]bool{"img": true}, checkDetachErr: errors.New("this driver cannot hold a background command")}
		box, err := newReal("", fd, drv).Boot(actions.BootSpec{
			Name:   "inmem",
			Recipe: recipe.Recipe{Image: recipe.ImageRef{Name: "img"}, Command: []string{"sh"}},
		})
		if err != nil {
			t.Fatalf("Boot: %v", err)
		}
		if box.ID == "" || box.Instance == "" {
			t.Fatalf("Boot must return an identity, got %+v", box)
		}
	})
}

// CONTRACT: a chosen node name is the box's id, so the caller can reap by the
// name it picked.
func TestBootUsesChosenNodeName(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	box, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name:     "inmem",
		NodeName: "picked",
		Recipe:   recipe.Recipe{Image: recipe.ImageRef{Name: "img"}, Command: []string{"sh"}},
	})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if box.ID != "picked" {
		t.Fatalf("box id = %q, want the chosen name", box.ID)
	}
}

// CONTRACT: the value meets the SAME post-parse gate a dabs.yaml recipe does.
// A library entry point is not a way past the checks the CLI enforces — each
// case here is refused when written in YAML, so it is refused as a value.
func TestBootValidatesTheRecipeValue(t *testing.T) {
	cases := []struct {
		name string
		rec  recipe.Recipe
		want string
	}{{
		name: "two sources at the same box path",
		rec: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Sources: []recipe.Source{{Mount: "/a", Path: "/work"}, {Mount: "/b", Path: "/work"}},
		},
		want: "each box path must be unique",
	}, {
		name: "control character in an env value",
		rec: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Env:     map[string]string{"A": "b\x1b[2Jc"},
		},
		want: "control character",
	}, {
		name: "control character in a source path",
		rec: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Sources: []recipe.Source{{Mount: "/a", Path: "/work\n"}},
		},
		want: "control character",
	}, {
		name: "egress allow and deny together",
		rec: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Egress:  recipe.Egress{Mode: recipe.EgressProxy, Allow: []string{"a.com"}, Deny: []string{"b.com"}},
		},
		want: "mutually exclusive",
	}, {
		name: "malformed egress pattern",
		rec: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Egress:  recipe.Egress{Mode: recipe.EgressProxy, Allow: []string{"https://a.com/x"}},
		},
		want: "allow",
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd := baseData()
			fd.exists["/a"], fd.exists["/b"] = true, true
			drv := &fakeDriver{built: map[string]bool{"img": true}}
			_, err := newReal("", fd, drv).Boot(actions.BootSpec{Name: "inmem", Recipe: c.rec})
			if err == nil {
				t.Fatalf("want a refusal naming %q, got none", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
			if len(drv.ups) != 0 {
				t.Errorf("a refused recipe must not bring a box up: %v", drv.ups)
			}
		})
	}
}

// CONTRACT: the boot name becomes a node id and a directory under ~/.dabs/nodes,
// so it takes an id's shape. A traversal, a separator, or a control byte is
// refused before anything is minted — the CLI's name is a vetted registry key,
// and a library caller's must be vetted too.
func TestBootRefusesANameThatIsNotAnID(t *testing.T) {
	for _, name := range []string{"../../escape", "a/b", "with space", "bad\nname", ".hidden", ""} {
		t.Run(name, func(t *testing.T) {
			fd := baseData()
			drv := &fakeDriver{built: map[string]bool{"img": true}}
			_, err := newReal("", fd, drv).Boot(actions.BootSpec{
				Name:   name,
				Recipe: recipe.Recipe{Image: recipe.ImageRef{Name: "img"}, Command: []string{"sh"}},
			})
			if err == nil {
				t.Fatalf("name %q must be refused", name)
			}
			if len(drv.ups) != 0 {
				t.Errorf("a refused name must not bring a box up: %v", drv.ups)
			}
			for path := range fd.dirs {
				if strings.Contains(path, "escape") {
					t.Errorf("a traversing name reached the filesystem: %s", path)
				}
			}
		})
	}
}

// CONTRACT: a `worktree:` source is provisioned for the ORIGIN IT NAMES. A Go
// caller names an absolute repo path, and the box must stand on a fresh checkout
// of it — never on the repository itself. A bind mount of the origin would hand
// the caller a live tree while it believed it had an isolated one.
func TestBootWorktreeFromAbsoluteOriginCutsACheckout(t *testing.T) {
	fd := baseData()
	fd.toplevel["/repo"] = nil // /repo is a git repo whose root is itself
	fd.exists["/repo"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if _, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name: "inmem",
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Sources: []recipe.Source{{Worktree: "/repo", Path: "/work"}},
		},
	}); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if len(fd.worktrees) != 1 {
		t.Fatalf("want one worktree cut off /repo, got %v", fd.worktrees)
	}
	work := mountAt(t, onlyUp(t, drv), "/work")
	if work.Host == "/repo" {
		t.Fatalf("the repository itself is bound at /work — a worktree source degraded to a live mount")
	}
	if work.Host != fd.worktrees[0] {
		t.Fatalf("/work mounts %q, want the cut checkout %q", work.Host, fd.worktrees[0])
	}
}

// CONTRACT: a `copy:` source is snapshotted for the origin it names, whatever
// that origin is. The box gets the snapshot dabs owns, never a live bind of the
// directory the caller asked to be copied.
func TestBootCopyFromAbsoluteOriginSnapshots(t *testing.T) {
	fd := baseData()
	fd.exists["/data"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if _, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name: "inmem",
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Sources: []recipe.Source{{Copy: "/data", Path: "/work"}},
		},
	}); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if len(fd.copies) != 1 || !strings.HasPrefix(fd.copies[0], "/data -> ") {
		t.Fatalf("want one snapshot of /data, got %v", fd.copies)
	}
	work := mountAt(t, onlyUp(t, drv), "/work")
	if work.Host == "/data" {
		t.Fatalf("/data is bound live at /work — a copy source degraded to a live mount")
	}
	if !strings.HasPrefix(work.Host, "/home/t/.dabs/nodes/") {
		t.Fatalf("/work mounts %q, want the snapshot dabs owns", work.Host)
	}
}

// CONTRACT: a `worktree:` source that CANNOT be provisioned refuses by name. The
// one outcome it may never have is a box that came up with the origin bound live,
// which is the isolation the caller asked for silently withdrawn.
func TestBootWorktreeThatCannotBeProvisionedRefuses(t *testing.T) {
	fd := baseData() // /repo is not registered as a repo → GitToplevel errors
	fd.exists["/repo"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	_, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name: "inmem",
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Sources: []recipe.Source{{Worktree: "/repo", Path: "/work"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not a git") {
		t.Fatalf("want a refusal naming the repo, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Fatalf("a worktree that could not be cut still brought a box up: %v", drv.ups)
	}
}

// CONTRACT: a boot BOUND to an existing worktree attaches it, whatever the
// spelling of the `worktree:` origin the recipe carries. That is how a second
// boot lands in the checkout a first one minted instead of cutting another and
// abandoning the work in it.
func TestBootBindsAnExistingWorktreeOverAnAbsoluteOrigin(t *testing.T) {
	fd := baseData()
	fd.toplevel["/repo"] = nil
	fd.exists["/repo"] = true
	wt := seedWorktreeNode(fd, "repo-a1b2c3d4", wtState{branch: "dabs/a1b2c3d4"})
	fd.exists["/repo/.git"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if _, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name:     "inmem",
		Worktree: "repo-a1b2c3d4",
		Recipe: recipe.Recipe{
			Image:   recipe.ImageRef{Name: "img"},
			Command: []string{"sh"},
			Sources: []recipe.Source{{Worktree: "/repo", Path: "/work"}},
		},
	}); err != nil {
		t.Fatalf("Boot onto an existing worktree: %v", err)
	}
	if len(fd.worktrees) != 0 {
		t.Fatalf("a bound boot cut a fresh worktree: %v", fd.worktrees)
	}
	if got := mountAt(t, onlyUp(t, drv), "/work").Host; got != wt {
		t.Fatalf("/work mounts %q, want the bound checkout %q", got, wt)
	}
}

// CONTRACT: a boot hands back the node it STANDS ON, not only the box node it
// minted. The box node is per-run; the place outlives it, and naming that place
// on a later boot is what lands the second run in the first one's checkout
// instead of cutting a fresh worktree over its uncommitted work.
func TestBootReportsThePlaceItStandsOn(t *testing.T) {
	fd := baseData()
	fd.toplevel["/repo"] = nil
	fd.exists["/repo"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	rec := recipe.Recipe{
		Image:   recipe.ImageRef{Name: "img"},
		Command: []string{"sh"},
		Sources: []recipe.Source{{Worktree: "/repo", Path: "/work"}},
	}
	first, err := newReal("", fd, drv).Boot(actions.BootSpec{Name: "inmem", Recipe: rec})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if first.Parent == "" || first.Parent == first.ID {
		t.Fatalf("boot reported no place of its own: %+v", first)
	}
	// The place it named is a worktree node, and it holds the checkout the box got.
	fd.commondir = map[string]string{nodeBase + "/" + first.Parent + "/held/worktree": "/repo/.git"}
	fd.exists["/repo/.git"] = true
	fd.exists[nodeBase+"/"+first.Parent+"/held/worktree"] = true
	cut := mountAt(t, onlyUp(t, drv), "/work").Host

	// A second boot naming that place re-enters it: nothing new is cut, and the
	// box lands on the same checkout.
	again, err := newReal("", fd, drv).Boot(actions.BootSpec{Name: "inmem", Recipe: rec, Worktree: first.Parent})
	if err != nil {
		t.Fatalf("re-boot onto the reported place: %v", err)
	}
	if len(fd.worktrees) != 1 {
		t.Fatalf("the re-boot cut another worktree: %v", fd.worktrees)
	}
	if again.Parent != first.Parent {
		t.Errorf("re-boot stands on %q, want the same place %q", again.Parent, first.Parent)
	}
	if got := mountAt(t, drv.ups[1], "/work").Host; got != cut {
		t.Errorf("re-boot mounts %q, want the first boot's checkout %q", got, cut)
	}
}

// mountAt returns the box's mount landing at path, failing when there is none.
func mountAt(t *testing.T, spec sandbox.Spec, path string) sandbox.Mount {
	t.Helper()
	for _, m := range spec.Mounts {
		if m.Path == path {
			return m
		}
	}
	t.Fatalf("no mount at %s: %+v", path, spec.Mounts)
	return sandbox.Mount{}
}

// CONTRACT: a recipe with no image provisions a PLACE, not a box, so there is no
// identity to return — Boot refuses rather than returning an empty Box.
func TestBootRefusesABoxlessRecipe(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{}
	_, err := newReal("", fd, drv).Boot(actions.BootSpec{
		Name:   "inmem",
		Recipe: recipe.Recipe{Command: []string{"sh"}},
	})
	if err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("want a no-image refusal, got %v", err)
	}
}

var _ sandbox.Driver = (*fakeDriver)(nil)
