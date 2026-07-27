package actions_test

// Component tests for the LIBRARY entry point: booting a box from a recipe
// VALUE. Driven through the public API with the driver and data seams faked, so
// the refusals are pinned without a real box.

import (
	"strings"
	"testing"

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
