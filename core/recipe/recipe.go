// Package recipe is the named-box registry behind `dabs recipe <name>`. A
// recipe is a fully declarative box: an image, what to mount/copy into it, its
// env, and the command to run. Everything a box does is visible in the recipe —
// nothing is hardcoded in Go. `dabs recipe claude` is just the bundled `claude`
// recipe; the same box is reproducible by hand as a plain dabs up + dabs run.
//
// The registry is YAML (so it can carry comments) with a single top-level
// `recipes:` map. It is the bundled default merged with the user's
// ~/.dabs/recipes.yaml (user entries win).
package recipe

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Registry is a recipes file: a top-level `recipes:` map and an optional
// `default:` naming the recipe `dabs recipe` runs when given no name.
type Registry struct {
	Default string            `json:"default,omitempty"`
	Recipes map[string]Recipe `json:"recipes"`
}

// Recipe is one named box.
type Recipe struct {
	Image   ImageRef          `json:"image"`             // the box image (name to reuse, or a build recipe)
	Workdir string            `json:"workdir,omitempty"` // default /work
	Command []string          `json:"command,omitempty"` // what `dabs recipe` runs in the box
	Env     map[string]string `json:"env,omitempty"`     // environment inside the box
	Sources []Source          `json:"sources,omitempty"` // what lands in the box, and how
}

// ImageRef is a union: either a bare image NAME (reuse ~/.dabs/images/<name>,
// building it from a bundled recipe if missing) or an inline build recipe
// ({dockerfile, context}). In YAML it is written either as a string or a map.
type ImageRef struct {
	Name       string `json:"name,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Context    string `json:"context,omitempty"`
}

// UnmarshalJSON accepts either "image: claude" or "image: {dockerfile: …}".
// (sigs.k8s.io/yaml routes YAML through encoding/json, so this covers both.)
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

// Source is one thing placed into the box at Path. Exactly one of Mount /
// Worktree / Copy names the host origin and picks HOW it lands:
//   - mount:    a live bind — the box's writes hit the host (vault, pairing).
//   - worktree: a fresh git branch off HEAD of the named repo, mounted live.
//   - copy:     a snapshot taken at up time — the box owns it, host untouched.
//
// Host paths may use ~ and $VAR/${VAR}; they are expanded at load-adjacent time.
type Source struct {
	Mount    string `json:"mount,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Copy     string `json:"copy,omitempty"`
	Path     string `json:"path"`         // absolute destination inside the box
	RO       bool   `json:"ro,omitempty"` // for mount: read-only
}

// Kind returns which of the three source strategies this entry uses, plus the
// host origin. An entry that names none (or more than one) is invalid.
func (s Source) Kind() (kind, origin string, err error) {
	set := map[string]string{}
	if s.Mount != "" {
		set["mount"] = s.Mount
	}
	if s.Worktree != "" {
		set["worktree"] = s.Worktree
	}
	if s.Copy != "" {
		set["copy"] = s.Copy
	}
	if len(set) != 1 {
		return "", "", fmt.Errorf("source for %q must set exactly one of mount/worktree/copy", s.Path)
	}
	for k, v := range set {
		kind, origin = k, v
	}
	if s.Path == "" {
		return "", "", fmt.Errorf("source %s:%s missing box path", kind, origin)
	}
	return kind, origin, nil
}

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
func Parse(data []byte) (Registry, error) {
	var reg Registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return Registry{}, err
	}
	if reg.Recipes == nil {
		reg.Recipes = map[string]Recipe{}
	}
	return reg, nil
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
