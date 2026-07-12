package recipe_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/recipe"
)

// B11: an unknown or misspelled key must be rejected, not silently dropped, so
// a `commnd:` typo does not yield a recipe with no command and a `banana:` is
// not swallowed. Strictness applies to top-level, recipe, and source fields.
func TestUnknownKeysRejected(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		key  string // the offending key the error should name
	}{
		{"recipe field typo", "recipes:\n  r:\n    image: alpine\n    commnd: [sh]\n", "commnd"},
		{"recipe field unknown", "recipes:\n  r:\n    image: alpine\n    banana: 3\n", "banana"},
		{"top-level unknown", "banana: 3\nrecipes:\n  r:\n    image: alpine\n", "banana"},
		{"source field unknown", "recipes:\n  r:\n    image: alpine\n    sources:\n    - mount: /a\n      path: /work\n      banana: 3\n", "banana"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := recipe.Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("want error naming %q, got nil", c.key)
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Fatalf("error %q should name the offending key %q", err, c.key)
			}
		})
	}
}

// B23: YAML type-coercion must not rename recipes. An unquoted `off:`/`yes:`/
// `1.0:` key resolves to a bool/number in stock YAML; the recipe name must keep
// the literal text a human wrote so `dabs recipe off` finds it.
func TestRecipeNamesKeepLiteralText(t *testing.T) {
	for _, name := range []string{"off", "yes", "no", "on", "1.0", "true"} {
		t.Run(name, func(t *testing.T) {
			reg, err := recipe.Parse([]byte("recipes:\n  " + name + ":\n    image: alpine\n    command: [sh]\n"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reg.Get(name); err != nil {
				t.Fatalf("recipe %q not found under its literal name: %v (known: %v)", name, err, reg.Names())
			}
		})
	}
}

// B24: a null recipe key must be a clean user-level error, not a Go-internal
// leak (`unsupported map key of type: %!s(<nil>)`), and must not take down the
// parse with an internal message. A structured key is rejected the same way.
func TestBadRecipeKeyIsCleanError(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"null key", "recipes:\n  null:\n    image: alpine\n"},
		{"structured key", "recipes:\n  ? [a, b]\n  : {image: alpine}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := recipe.Parse([]byte(c.yaml))
			if err == nil {
				t.Fatal("want an error for a non-text recipe key, got nil")
			}
			for _, leak := range []string{"%!s", "<nil>", "unsupported map key"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error leaks Go internals (%q): %v", leak, err)
				}
			}
			if !strings.Contains(err.Error(), "plain text") {
				t.Fatalf("error should tell the user recipe names must be plain text: %v", err)
			}
		})
	}
}

// B22 / B25 / B32: a control character reaching the terminal raw (ESC to move
// the cursor, a newline to split the ls tree or blank an env var) must be
// rejected at parse. The rejection covers recipe names, source paths, env keys,
// and env values — and the error itself must not echo the raw byte (it uses %q
// so it can't re-inject).
func TestControlCharactersRejected(t *testing.T) {
	const esc = "\x1b" // what a double-quoted \e / \x1b decodes to
	cases := []struct {
		name string
		yaml string
	}{
		// B22: ESC via a double-quoted YAML escape in a recipe name.
		{"esc in name", "recipes:\n  \"a\\eb\":\n    image: alpine\n"},
		// B25: a newline embedded in a recipe name.
		{"newline in name", "recipes:\n  \"a\\nb\":\n    image: alpine\n"},
		// B22: ESC in a source path.
		{"esc in source path", "recipes:\n  r:\n    image: alpine\n    sources:\n    - mount: /a\n      path: \"/w\\eb\"\n"},
		// B22: ESC in an env key.
		{"esc in env key", "recipes:\n  r:\n    image: alpine\n    env:\n      \"A\\eB\": v\n"},
		// B22: ESC in an env value.
		{"esc in env value", "recipes:\n  r:\n    image: alpine\n    env:\n      A: \"v\\ew\"\n"},
		// B32: a newline in an env value (would silently blank the variable).
		{"newline in env value", "recipes:\n  r:\n    image: alpine\n    env:\n      A: \"line1\\nline2\"\n"},
		// B32: a NUL in an env value.
		{"nul in env value", "recipes:\n  r:\n    image: alpine\n    env:\n      A: \"a\\x00b\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := recipe.Parse([]byte(c.yaml))
			if err == nil {
				t.Fatal("want a rejection for a control character, got nil")
			}
			if !strings.Contains(err.Error(), "control character") {
				t.Fatalf("error should name the control character: %v", err)
			}
			// The error must not carry a raw ESC (or other raw control byte),
			// or reading it re-injects the escape sequence into the terminal.
			if strings.ContainsAny(err.Error(), esc+"\n\x00") {
				t.Fatalf("error text contains a RAW control byte and could re-inject: %q", err.Error())
			}
		})
	}
}

// A clean recipe with ordinary env values and paths still parses — the
// control-character gate must not reject legitimate content.
func TestCleanRecipeStillParses(t *testing.T) {
	reg, err := recipe.Parse([]byte("recipes:\n  r:\n    image: alpine\n    command: [sh]\n    env:\n      PATH: /usr/bin\n      GREETING: hello world\n    sources:\n    - mount: .\n      path: /work\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Recipes["r"].Env["GREETING"]; got != "hello world" {
		t.Fatalf("env GREETING = %q, want %q", got, "hello world")
	}
}

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

// description: is an optional one-line human summary that round-trips through parse.
func TestDescriptionParses(t *testing.T) {
	reg, err := recipe.Parse([]byte("recipes:\n  r:\n    description: a clean shell box\n    image: alpine\n    command: [sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Recipes["r"].Description; got != "a clean shell box" {
		t.Errorf("Description = %q, want %q", got, "a clean shell box")
	}
	// omitempty: a recipe without one parses to the empty string, not an error.
	reg2, err := recipe.Parse([]byte("recipes:\n  r:\n    image: alpine\n    command: [sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg2.Recipes["r"].Description; got != "" {
		t.Errorf("missing Description = %q, want empty", got)
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
		{"mkmount", recipe.Source{Mkmount: "/a", Path: "/work"}, "mkmount", ""},
		{"copy", recipe.Source{Copy: "/a", Path: "/work"}, "copy", ""},
		{"worktree", recipe.Source{Worktree: ".", Path: "/work"}, "worktree", ""},
		{"none", recipe.Source{Path: "/work"}, "", "exactly one"},
		{"two", recipe.Source{Mount: "/a", Copy: "/b", Path: "/work"}, "", "exactly one"},
		// A source with no box path is a source for a recipe that makes a PLACE and
		// no box — there is nowhere for it to land, and that is not an error.
		{"no path", recipe.Source{Mount: "/a"}, "mount", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, _, err := c.src.Kind()
			if c.name == "no path" && !c.src.NeedsBoxPath() {
				t.Error("a source with no path must report NeedsBoxPath, so a box recipe can refuse it")
			}
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
