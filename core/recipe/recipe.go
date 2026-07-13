// Package recipe is the named-box registry behind `dabs recipe <name>`. A
// recipe is a fully declarative box: an image, what to mount/copy into it, its
// env, and the command to run. Everything a box does is visible in the recipe —
// nothing is hardcoded in Go. `dabs recipe sh` is just the bundled `sh`
// recipe; the same box is reproducible by hand as a plain dabs recipe --detach + dabs exec.
//
// The registry is YAML (so it can carry comments) with a single top-level
// `recipes:` map. It is the bundled default merged with the user's
// ~/.dabs/recipes.yaml (user entries win).
package recipe

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v2"
)

// Registry is a recipes file: a top-level `recipes:` map and an optional
// `default:` naming the recipe `dabs recipe` runs when given no name.
type Registry struct {
	Default string            `json:"default,omitempty" yaml:"default,omitempty"`
	Recipes map[string]Recipe `json:"recipes" yaml:"recipes"`
}

// Recipe is one named box.
type Recipe struct {
	Description string            `json:"description,omitempty" yaml:"description,omitempty"` // one-line human summary, shown in `dabs recipes`
	Image       ImageRef          `json:"image" yaml:"image"`                                 // the box image (name to reuse, or a build recipe)
	Workdir     string            `json:"workdir,omitempty" yaml:"workdir,omitempty"`         // default /work
	Command     []string          `json:"command,omitempty" yaml:"command,omitempty"`         // what runs in the box
	Env         map[string]string `json:"env,omitempty" yaml:"env,omitempty"`                 // environment inside the box
	Sources     []Source          `json:"sources,omitempty" yaml:"sources,omitempty"`         // what lands in the box, and how
	Target      string            `json:"target,omitempty" yaml:"target,omitempty"`           // which fleet driver runs it (e.g. "docker", a server); default local
	Keep        bool              `json:"keep,omitempty" yaml:"keep,omitempty"`               // keep the box alive after the command (default: delete it)
}

// ImageRef is a union: either a bare image NAME (reuse ~/.dabs/images/<name>,
// building it from a bundled recipe if missing) or an inline build recipe
// ({dockerfile, context}). In YAML it is written either as a string or a map.
type ImageRef struct {
	Name       string `json:"name,omitempty" yaml:"name,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
	Context    string `json:"context,omitempty" yaml:"context,omitempty"`
}

// UnmarshalJSON accepts either a bare name string or a {dockerfile,context}
// object. It covers the path that decodes a Registry from JSON (a recipe sent
// to a server as JSON, which is also valid YAML).
func (r *ImageRef) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		r.Name = s
		return nil
	}
	type raw ImageRef // avoid recursion
	var o raw
	if err := json.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("image: want a name string or {dockerfile,context}: %w", err)
	}
	*r = ImageRef(o)
	return nil
}

// UnmarshalYAML accepts either "image: claude" (a scalar) or
// "image: {dockerfile: …}" (a mapping), matching UnmarshalJSON for the YAML
// decode path.
func (r *ImageRef) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		r.Name = s
		return nil
	}
	type raw ImageRef // avoid recursion
	var o raw
	if err := unmarshal(&o); err != nil {
		return fmt.Errorf("image: want a name string or {dockerfile,context}: %w", err)
	}
	*r = ImageRef(o)
	return nil
}

// Source is one thing placed into the box at Path. Exactly one of Mount /
// Mkmount / Worktree / Copy names the origin and picks HOW it lands:
//   - mount:    a live bind — the box's writes hit the host. The host path must
//     exist; a missing one is a typo, not an instruction.
//   - mkmount:  a live bind that CREATES the host path first (0700) if it is not
//     there. Say it where you mean "provision this": a login dir a harness will
//     fill, a session dir that starts empty.
//   - worktree: a fresh git branch off HEAD of the named repo, mounted live.
//   - copy:     a snapshot taken at up time — the box owns it, host untouched.
//
// Host paths may use ~ and $VAR/${VAR}. dabs supplies the running box's node
// spaces as variables, so a source can point at them without knowing an id:
//
//	$NODE_VOLUME  survives rm (unless --volume) — sessions, caches
//	$NODE_HELD    rm asks first               — work you would miss
//	$NODE_TMP     rm reaps quietly            — scratch
//
// $NODE_EPHEMERAL is a permanent alias for $NODE_HELD (the held space's former
// name), so a recipe written before the rename keeps working unchanged.
//
// A mkmount into $NODE_VOLUME nested over a shared mount gives the box its own
// persistent slice of an otherwise shared tree.
type Source struct {
	Mount    string `json:"mount,omitempty" yaml:"mount,omitempty"`
	Mkmount  string `json:"mkmount,omitempty" yaml:"mkmount,omitempty"`
	Worktree string `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	Copy     string `json:"copy,omitempty" yaml:"copy,omitempty"`
	// At is where a source that PROVISIONS something puts it on the host — a
	// worktree's checkout, a copy's directory. It names one of the new node's own
	// spaces ($NODE_HELD/worktree), so the recipe says where the bytes land
	// and what `rm` will do to them, rather than dabs knowing in secret.
	At   string `json:"at,omitempty" yaml:"at,omitempty"`
	Path string `json:"path" yaml:"path"`                 // absolute destination inside the box
	RO   bool   `json:"ro,omitempty" yaml:"ro,omitempty"` // for mount: read-only
}

// Kind returns which source strategy this entry uses, plus its host origin. An
// entry that names none (or more than one) is invalid.
func (s Source) Kind() (kind, origin string, err error) {
	set := map[string]string{}
	if s.Mount != "" {
		set["mount"] = s.Mount
	}
	if s.Mkmount != "" {
		set["mkmount"] = s.Mkmount
	}
	if s.Worktree != "" {
		set["worktree"] = s.Worktree
	}
	if s.Copy != "" {
		set["copy"] = s.Copy
	}
	if len(set) != 1 {
		return "", "", fmt.Errorf("source for %q must set exactly one of mount/mkmount/worktree/copy", s.Path)
	}
	for k, v := range set {
		kind, origin = k, v
	}
	return kind, origin, nil
}

// NeedsBoxPath reports whether this source must say where it lands in a box. A
// recipe with an image puts its sources somewhere; a recipe without one only
// makes places, and a place has nowhere to land.
func (s Source) NeedsBoxPath() bool { return s.Path == "" }

// Get resolves a recipe by name, or errors with the list of known names — so a
// caller (usually an agent) that guessed wrong sees the real options.
func (reg Registry) Get(name string) (Recipe, error) {
	rec, ok := reg.Recipes[name]
	if !ok {
		return Recipe{}, fmt.Errorf("no recipe %q (known: %s)", name, strings.Join(reg.Names(), ", "))
	}
	return rec, nil
}

// Names returns the known recipe names, sorted.
func (reg Registry) Names() []string {
	names := make([]string, 0, len(reg.Recipes))
	for n := range reg.Recipes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Parse decodes a YAML recipes registry. IO (reading bundled bytes and the
// user's ~/.dabs/recipes.yaml) is the caller's job — done through the data seam
// — so this stays pure and testable.
//
// Decoding is strict: UnmarshalStrict rejects an unknown or misspelled key
// (`commnd:` instead of `command:`) instead of silently dropping it, and recipe
// names keep the literal text a human wrote — a bare `off:`/`yes:`/`1.0:` key
// stays "off"/"yes"/"1.0" rather than YAML-coercing to false/true/1. A null or
// structured recipe key is a user-level error, and validate rejects control
// characters that would otherwise reach the terminal raw.
func Parse(data []byte) (Registry, error) {
	if err := checkRecipeKeys(data); err != nil {
		return Registry{}, err
	}
	var reg Registry
	if err := yaml.UnmarshalStrict(data, &reg); err != nil {
		return Registry{}, err
	}
	if reg.Recipes == nil {
		reg.Recipes = map[string]Recipe{}
	}
	if err := validate(reg); err != nil {
		return Registry{}, err
	}
	return reg, nil
}

// checkRecipeKeys rejects a recipe map key that is not plain text: a null key
// (`null:`) or a structured key (a mapping/sequence). Decoding into
// map[string]Recipe turns such a key into an empty or unusable name rather than
// reporting it, so the whole file's recipes come out wrong. Reading the keys
// with their YAML-resolved types keeps the error user-level instead of leaking
// a Go-internal map-key message.
func checkRecipeKeys(data []byte) error {
	var raw struct {
		Recipes yaml.MapSlice `yaml:"recipes"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, item := range raw.Recipes {
		if item.Key == nil {
			return fmt.Errorf("a recipe name is null (`null:`); recipe names must be plain text")
		}
		if k := reflect.ValueOf(item.Key).Kind(); k == reflect.Map || k == reflect.Slice {
			return fmt.Errorf("a recipe name is a structured key (a mapping or sequence); recipe names must be plain text")
		}
	}
	return nil
}

// validate is the single post-parse gate. It rejects ASCII control characters
// (0x00–0x1F, 0x7F) in the values a hostile recipes file controls and that dabs
// later prints raw or passes into a box: recipe names, source paths, env keys,
// and env values. Left in, an ESC could move the terminal cursor from a
// `dabs recipes` listing or a `known: …` error, a newline in a name would split
// the ls tree into phantom rows, and a newline in an env value silently blanks
// the whole variable inside the box. The error text uses %q so the offending
// byte escapes rather than re-injecting through the message itself.
func validate(reg Registry) error {
	for name, rec := range reg.Recipes {
		if err := rejectControl(fmt.Sprintf("recipe name %q", name), name); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, s := range rec.Sources {
			if err := rejectControl(fmt.Sprintf("source path in recipe %q", name), s.Path); err != nil {
				return err
			}
			// Two sources landing at the SAME box path silently mask each other —
			// whichever binds last wins and the other never appears. Reject the
			// exact-duplicate destination so the conflict is named, not hidden.
			// Nesting at DIFFERENT paths stays legal; an empty path is a source
			// that only makes a place and lands nowhere, so it is not a collision.
			if s.Path != "" {
				if seen[s.Path] {
					return fmt.Errorf("recipe %q has two sources mounting to the same box path %q; each box path must be unique", name, s.Path)
				}
				seen[s.Path] = true
			}
		}
		for k, v := range rec.Env {
			if err := rejectControl(fmt.Sprintf("env key in recipe %q", name), k); err != nil {
				return err
			}
			if err := rejectControl(fmt.Sprintf("value of env %q in recipe %q", k, name), v); err != nil {
				return err
			}
		}
	}
	return nil
}

// rejectControl fails if s holds an ASCII control byte. %q in the message
// escapes the byte so the error cannot itself carry a raw ESC to the terminal.
func rejectControl(what, s string) error {
	for _, r := range s {
		if r <= 0x1f || r == 0x7f {
			return fmt.Errorf("%s contains a disallowed control character: %q", what, s)
		}
	}
	return nil
}

// Merge overlays other onto reg: other's recipes win by name, and its `default`
// (if set) takes over. This is the precedence chain bundled → ~/.dabs →
// local dabs.yaml, each merged onto the last.
func (reg *Registry) Merge(other Registry) {
	maps.Copy(reg.Recipes, other.Recipes)
	if other.Default != "" {
		reg.Default = other.Default
	}
}
