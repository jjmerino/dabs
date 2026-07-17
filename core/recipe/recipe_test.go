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
	// The box recipes carry an image and a command; the place recipes (`wt`,
	// `scratch`) are image-less and command-less BY DESIGN — they make a place
	// and stop, so anything runnable on them would boot something. Each source
	// must be the kind the recipe's name promises.
	for _, want := range []struct {
		name       string
		box        bool
		sourceKind string
	}{
		{"sh", true, "mount"},
		{"wt", false, "worktree"},
		{"wtbox", true, "worktree"},
		{"scratch", false, "copy"},
		{"scratchbox", true, "copy"},
	} {
		rec, err := reg.Get(want.name)
		if err != nil {
			t.Fatalf("bundled registry missing %q: %v", want.name, err)
		}
		hasImage := rec.Image.Name != "" || rec.Image.Dockerfile != ""
		if want.box && (len(rec.Command) == 0 || !hasImage) {
			t.Errorf("recipe %q must carry an image and a command — it could never run: %+v", want.name, rec)
		}
		if !want.box && (len(rec.Command) != 0 || hasImage) {
			t.Errorf("recipe %q must be image-less and command-less (it makes a place, no box): %+v", want.name, rec)
		}
		if len(rec.Sources) == 0 {
			t.Fatalf("recipe %q has no sources", want.name)
		}
		for _, s := range rec.Sources {
			if k, _, err := s.Kind(); err != nil || k != want.sourceKind {
				t.Errorf("recipe %q source must be a %s, got %q (%v)", want.name, want.sourceKind, k, err)
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

// An egress proxy chain is ordered box→internet; the ORDER is the trust order,
// so it must survive parsing, and each hop classifies as tls or module.
func TestProxyChainParses(t *testing.T) {
	reg, err := recipe.Parse([]byte(`
recipes:
  claude:
    image: claude
    egress:
      http_proxy:
        - tls: terminate
        - module: $RECIPE_DIR/recorder.ts
        - module: $RECIPE_DIR/responder.ts
        - tls: originate
`))
	if err != nil {
		t.Fatal(err)
	}
	p := reg.Recipes["claude"].Egress.HTTPProxy
	if len(p) != 4 {
		t.Fatalf("got %d hops, want 4", len(p))
	}
	if !p[0].IsTerminate() || !p[3].IsOriginate() {
		t.Errorf("hops 0/3 should be terminate/originate, got %q/%q", p[0].TLS, p[3].TLS)
	}
	if !p[1].IsModule() || p[1].Module != "$RECIPE_DIR/recorder.ts" || p[1].Label() != "recorder" {
		t.Errorf("hop 1 module/label = %q/%q", p[1].Module, p[1].Label())
	}
	if got := reg.Recipes["claude"].EgressMode(); got != recipe.EgressProxy {
		t.Errorf("EgressMode() = %q, want %q", got, recipe.EgressProxy)
	}
}

// A module hop carries its extra config; an explicit name overrides the basename.
func TestModuleHopConfigAndName(t *testing.T) {
	reg, err := recipe.Parse([]byte(`
recipes:
  r:
    image: x
    egress:
      http_proxy:
        - module: rec.ts
          name: logger
          dir: /tmp/cassettes
`))
	if err != nil {
		t.Fatal(err)
	}
	h := reg.Recipes["r"].Egress.HTTPProxy[0]
	if h.Label() != "logger" {
		t.Errorf("label = %q, want logger (name overrides basename)", h.Label())
	}
	if h.Config["dir"] != "/tmp/cassettes" {
		t.Errorf("config not carried: %#v", h.Config)
	}
}

// A chain with NO tls is valid — its module hops act on the connection
// (allow/deny/route), no decryption. This is the domain-policy use case.
func TestProxyChainWithoutTLSIsValid(t *testing.T) {
	_, err := recipe.Parse([]byte(`
recipes:
  r:
    image: x
    egress:
      http_proxy:
        - module: policy.ts
`))
	if err != nil {
		t.Fatalf("a tls-less chain should be valid (allow/deny hooks), got %v", err)
	}
}

// A hop key that is neither tls nor module is a typo, not a hop.
func TestUnknownHopKeyRejected(t *testing.T) {
	_, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      http_proxy:\n        - recorder: { module: rec.ts }\n"))
	if err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatalf("want unknown-key error mentioning tls/module, got %v", err)
	}
}

// tls boundaries must nest: no terminate-inside-terminate, no originate without a
// terminate, only terminate/originate directives. A trailing terminate (terminal
// window) is allowed.
func TestTLSBalanceValidation(t *testing.T) {
	cases := []struct{ name, chain, wantErr string }{
		{"nested terminate", "- tls: terminate\n        - tls: terminate", "already open"},
		{"originate without terminate", "- tls: originate", "without a preceding"},
		{"bad directive", "- tls: sideways", "terminate"},
		{"balanced ok", "- tls: terminate\n        - tls: originate", ""},
		{"terminate with hooks ok", "- tls: terminate\n        - module: h.ts\n        - tls: originate", ""},
		{"unclosed terminate rejected", "- tls: terminate\n        - module: h.ts", "must be closed by a `tls: originate`"},
		{"tls-less module ok", "- module: h.ts", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      http_proxy:\n        " + c.chain + "\n"))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// The egress mode is open|none, or a mapping with allow/deny/http_proxy; the
// default is open. A mapping needs at least one field; a bogus scalar or key is
// rejected; allow and deny are mutually exclusive.
func TestEgressModeValidation(t *testing.T) {
	cases := []struct{ name, egress, wantErr string }{
		{"open", "    egress: open\n", ""},
		{"none", "    egress: none\n", ""},
		{"unset defaults open", "", ""},
		{"proxy chain ok", "    egress:\n      http_proxy:\n        - module: h.ts\n", ""},
		{"proxy empty chain", "    egress:\n      http_proxy: []\n", "non-empty http_proxy chain"},
		{"allow list ok", "    egress:\n      allow: [api.example.com, '*.example.org']\n", ""},
		{"allow comma string ok", "    egress:\n      allow: \"api.example.com, *.example.org\"\n", ""},
		{"deny ok", "    egress:\n      deny: evil.example.com\n", ""},
		{"allow plus chain ok", "    egress:\n      allow: api.example.com\n      http_proxy:\n        - module: h.ts\n", ""},
		{"allow and deny exclusive", "    egress:\n      allow: a.com\n      deny: b.com\n", "mutually exclusive"},
		{"bad pattern", "    egress:\n      allow: \"a.*.com\"\n", "exact hostname"},
		{"bare star ok", "    egress:\n      deny: \"*\"\n", ""},
		{"empty mapping", "    egress: {}\n", "empty mapping"},
		{"bogus scalar", "    egress: sideways\n", "not one of open, none"},
		{"bogus mapping key", "    egress:\n      tunnel: []\n", "unknown key"},
		// A mis-keyed mapping whose value is NOT a hop list must still get the
		// friendly key diagnostic, not a raw decoder error.
		{"bogus key scalar value", "    egress:\n      foo: bar\n", "unknown key"},
		{"extra key alongside proxy", "    egress:\n      http_proxy:\n        - module: h.ts\n      extra: 1\n", "unknown key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n" + c.egress))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// allow/deny parse as a YAML list or a comma-separated string, onto the same
// fields; either form is proxy mode even with no http_proxy chain (the engine
// must run to enforce the gate).
func TestEgressAllowDenyParse(t *testing.T) {
	reg, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      allow: \" api.example.com , *.example.org \"\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Recipes["r"].Egress
	if len(e.Allow) != 2 || e.Allow[0] != "api.example.com" || e.Allow[1] != "*.example.org" {
		t.Errorf("comma allow = %#v, want the two trimmed patterns", e.Allow)
	}
	if got := reg.Recipes["r"].EgressMode(); got != recipe.EgressProxy {
		t.Errorf("EgressMode() = %q, want %q (an allow gate runs through the engine)", got, recipe.EgressProxy)
	}
	reg, err = recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      deny: [evil.example.com, '*']\n"))
	if err != nil {
		t.Fatal(err)
	}
	e = reg.Recipes["r"].Egress
	if len(e.Deny) != 2 || e.Deny[0] != "evil.example.com" || e.Deny[1] != "*" {
		t.Errorf("list deny = %#v", e.Deny)
	}
}

// A `tls: terminate` may carry a `domains` list scoping which hosts are
// decrypted. The list parses onto the hop; `domains` on `originate` or a
// non-list value is a clear error.
func TestEgressTerminateDomainsParse(t *testing.T) {
	reg, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      http_proxy:\n        - tls: terminate\n          domains: [api.example.com, example.org]\n        - module: h.ts\n        - tls: originate\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	hops := reg.Recipes["r"].Egress.HTTPProxy
	if len(hops[0].Domains) != 2 || hops[0].Domains[0] != "api.example.com" || hops[0].Domains[1] != "example.org" {
		t.Errorf("domains not parsed: %+v", hops[0])
	}
	if _, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: h.ts\n        - tls: originate\n          domains: [x.com]\n")); err == nil || !strings.Contains(err.Error(), "only applies to") {
		t.Errorf("want error for domains on originate, got %v", err)
	}
	if _, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      http_proxy:\n        - tls: terminate\n          domains: nope\n")); err == nil || !strings.Contains(err.Error(), "list of hostnames") {
		t.Errorf("want error for non-list domains, got %v", err)
	}
}

// An unset egress resolves to open (full outbound): dabs is a dev tool, a box
// that silently reaches nowhere confuses more than it protects.
func TestEgressDefaultsToOpen(t *testing.T) {
	reg, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Recipes["r"].EgressMode(); got != recipe.EgressOpen {
		t.Errorf("unset egress = %q, want %q", got, recipe.EgressOpen)
	}
}

// Only one tls window per chain — multiple terminate…originate windows are
// rejected (the engine would silently merge them).
func TestSingleTLSWindow(t *testing.T) {
	_, err := recipe.Parse([]byte("recipes:\n  r:\n    image: x\n    egress:\n      http_proxy:\n        - tls: terminate\n        - tls: originate\n        - tls: terminate\n        - tls: originate\n"))
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("want single-window error, got %v", err)
	}
}
