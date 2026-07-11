package recipe_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/recipe"
)

// The bundled registry ships inside the binary — if it's malformed or a recipe
// is unrunnable, every user is broken. Assert the shipped asset is well-formed
// and the OOTB recipes we promise are present and complete.
func TestBundledRegistryIsValid(t *testing.T) {
	reg, err := recipe.Parse(recipe.Bundled)
	if err != nil {
		t.Fatalf("bundled recipes.yaml does not parse: %v", err)
	}
	for _, want := range []string{"sh"} {
		rec, err := reg.Get(want)
		if err != nil {
			t.Fatalf("bundled registry missing %q: %v", want, err)
		}
		if len(rec.Command) == 0 {
			t.Errorf("recipe %q has no command — it could never run", want)
		}
		if rec.Image.Name == "" && rec.Image.Dockerfile == "" {
			t.Errorf("recipe %q has no image", want)
		}
		for _, s := range rec.Sources {
			if _, _, err := s.Kind(); err != nil {
				t.Errorf("recipe %q has an invalid source: %v", want, err)
			}
		}
	}
}

// image: accepts either a bare name or an inline build recipe.
func TestImageRefUnion(t *testing.T) {
	asName, err := recipe.Parse([]byte("recipes:\n  r:\n    image: alpine\n    command: [sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := asName.Recipes["r"].Image; got.Name != "alpine" || got.Dockerfile != "" {
		t.Errorf("string image = %+v, want Name=alpine", got)
	}

	asBuild, err := recipe.Parse([]byte("recipes:\n  r:\n    image: {dockerfile: ./D, context: .}\n    command: [sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := asBuild.Recipes["r"].Image; got.Dockerfile != "./D" || got.Name != "" {
		t.Errorf("object image = %+v, want Dockerfile=./D", got)
	}
}

// Kind enforces exactly-one-of mount/worktree/copy, and a destination path.
func TestSourceKind(t *testing.T) {
	cases := []struct {
		name    string
		src     recipe.Source
		want    string // "" means expect an error
		wantErr string
	}{
		{"mount", recipe.Source{Mount: "/a", Path: "/work"}, "mount", ""},
		{"copy", recipe.Source{Copy: "/a", Path: "/work"}, "copy", ""},
		{"worktree", recipe.Source{Worktree: ".", Path: "/work"}, "worktree", ""},
		{"none", recipe.Source{Path: "/work"}, "", "exactly one"},
		{"two", recipe.Source{Mount: "/a", Copy: "/b", Path: "/work"}, "", "exactly one"},
		{"no path", recipe.Source{Mount: "/a"}, "", "box path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, _, err := c.src.Kind()
			if c.want == "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got kind=%q err=%v", c.wantErr, kind, err)
				}
				return
			}
			if err != nil || kind != c.want {
				t.Fatalf("want kind %q, got %q err=%v", c.want, kind, err)
			}
		})
	}
}
