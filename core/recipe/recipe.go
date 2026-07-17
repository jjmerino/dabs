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
	Egress      Egress            `json:"egress,omitempty" yaml:"egress,omitempty"`           // the box's outbound network: open | none | {proxy: [chain]}
}

// Egress is the box's outbound network — a union. As a scalar it is `open`
// (full outbound, the DEFAULT when unset) or `none` (no network). As a mapping
// `{proxy: [chain]}` it routes ALL egress through an ordered interception chain.
// The default is open: dabs is a dev tool, and a box that silently reaches
// nowhere confuses far more than it protects — locking egress down is an opt-in.
type Egress struct {
	Mode  string     `json:"mode,omitempty" yaml:"-"`  // open | none | proxy (empty → open)
	Proxy []ProxyHop `json:"proxy,omitempty" yaml:"-"` // the chain, box→internet, when Mode == proxy
}

// Egress modes.
const (
	EgressOpen  = "open"
	EgressNone  = "none"
	EgressProxy = "proxy"
)

// EgressMode resolves an unset mode to Open (full outbound).
func (e Egress) EgressMode() string {
	if e.Mode == "" {
		return EgressOpen
	}
	return e.Mode
}

// EgressMode is the recipe's resolved egress mode.
func (r Recipe) EgressMode() string { return r.Egress.EgressMode() }

// UnmarshalYAML accepts the scalar forms (`egress: open`) or the proxy mapping
// (`egress: {proxy: [ ... ]}`).
func (e *Egress) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		e.Mode = s
		return nil
	}
	// Validate the mapping's KEYS on a permissive decode first, so a mis-keyed
	// mapping gets the friendly "unknown key" diagnostic — decoding straight into
	// the hop list would fail with a raw decoder error whenever the value is not
	// itself a list (e.g. `egress: {foo: bar}`).
	var keys map[string]interface{}
	if err := unmarshal(&keys); err != nil {
		return fmt.Errorf("egress: want a mode string (open|none) or `{proxy: [ ... ]}`: %w", err)
	}
	if err := checkEgressMapKeys(keys); err != nil {
		return err
	}
	var m map[string][]ProxyHop
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("egress: `proxy` must be a list of hops: %w", err)
	}
	e.Mode, e.Proxy = EgressProxy, m[EgressProxy]
	return nil
}

// UnmarshalJSON mirrors UnmarshalYAML for the server's JSON decode path.
func (e *Egress) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		e.Mode = s
		return nil
	}
	var keys map[string]interface{}
	if err := json.Unmarshal(b, &keys); err != nil {
		return fmt.Errorf("egress: want a mode string or `{proxy: [ ... ]}`: %w", err)
	}
	if err := checkEgressMapKeys(keys); err != nil {
		return err
	}
	var m map[string][]ProxyHop
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("egress: `proxy` must be a list of hops: %w", err)
	}
	e.Mode, e.Proxy = EgressProxy, m[EgressProxy]
	return nil
}

// checkEgressMapKeys validates that a mapping egress carries exactly the `proxy`
// key. It runs on a type-agnostic key set so the diagnostic is the same friendly
// message whatever the value's shape.
func checkEgressMapKeys(keys map[string]interface{}) error {
	if len(keys) != 1 {
		return fmt.Errorf("egress: a mapping must hold exactly the `proxy` key")
	}
	if _, ok := keys[EgressProxy]; !ok {
		for k := range keys {
			return fmt.Errorf("egress: unknown key %q — a mapping egress is `{proxy: [ ... ]}`", k)
		}
	}
	return nil
}

// ProxyHop is one entry in an egress proxy chain, ordered box→internet (the
// trust order — a credential-injecting hop sits nearest the internet). It is
// EITHER a TLS boundary directive (`tls: terminate` decrypts the agent's TLS to
// open the plaintext window; `tls: originate` re-encrypts to the real upstream)
// OR a hook MODULE (`module: <path>` plus optional config). A module hop inside
// a terminate…originate window inspects decrypted content; outside one it acts
// on the connection (host/port) for allow/deny/route.
type ProxyHop struct {
	TLS     string                 `json:"tls,omitempty" yaml:"-"`     // "terminate" | "originate"; empty if a module hop
	Domains []string               `json:"domains,omitempty" yaml:"-"` // on `tls: terminate`: only these hosts are decrypted; others pass through
	Module  string                 `json:"module,omitempty" yaml:"-"`  // module path; empty if a tls hop
	Config  map[string]interface{} `json:"config,omitempty" yaml:"-"`  // a module hop's extra config (excludes `module`)
}

// TLS boundary directives.
const (
	tlsTerminate = "terminate"
	tlsOriginate = "originate"
)

// IsTLS / IsModule classify a hop; IsTerminate / IsOriginate name the boundary.
func (h ProxyHop) IsTLS() bool       { return h.TLS != "" }
func (h ProxyHop) IsModule() bool    { return h.Module != "" }
func (h ProxyHop) IsTerminate() bool { return h.TLS == tlsTerminate }
func (h ProxyHop) IsOriginate() bool { return h.TLS == tlsOriginate }

// Label is a module hop's identity in engine logs: its `name:` config if given,
// else the module file's basename without extension.
func (h ProxyHop) Label() string {
	if n, ok := h.Config["name"].(string); ok && n != "" {
		return n
	}
	base := h.Module
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".ts")
	base = strings.TrimSuffix(base, ".js")
	return base
}

// UnmarshalYAML decodes one chain entry: a mapping keyed by `tls` (a directive)
// or `module` (a hook + its config).
func (h *ProxyHop) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var m map[string]interface{}
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("proxy hop: want a mapping like `tls: terminate` or `module: path`: %w", err)
	}
	return h.set(m)
}

// UnmarshalJSON mirrors UnmarshalYAML for the JSON decode path.
func (h *ProxyHop) UnmarshalJSON(b []byte) error {
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("proxy hop: want an object like {\"tls\":\"terminate\"} or {\"module\":\"path\"}: %w", err)
	}
	return h.set(m)
}

// set classifies a hop mapping as a tls directive or a module hook, normalizing
// nested maps so Config round-trips to JSON for the engine.
func (h *ProxyHop) set(m map[string]interface{}) error {
	nv, err := normalizeYAML(m)
	if err != nil {
		return err
	}
	m = nv.(map[string]interface{})
	if tv, ok := m[proxyTLS]; ok {
		s, ok := tv.(string)
		if !ok {
			return fmt.Errorf("proxy hop: `tls` must be %q or %q", tlsTerminate, tlsOriginate)
		}
		h.TLS = s
		// A `tls: terminate` may scope interception to a `domains` list — only
		// those hosts are decrypted; everything else passes through untouched.
		for k, v := range m {
			switch k {
			case proxyTLS:
			case proxyDomains:
				ds, err := toStringList(v)
				if err != nil {
					return fmt.Errorf("proxy hop: `domains` must be a list of hostnames: %w", err)
				}
				h.Domains = ds
			default:
				return fmt.Errorf("proxy hop: a `tls` directive takes only an optional `domains` list, not %q", k)
			}
		}
		if len(h.Domains) > 0 && s != tlsTerminate {
			return fmt.Errorf("proxy hop: `domains` only applies to `tls: terminate`")
		}
		return nil
	}
	if mv, ok := m[proxyModule]; ok {
		s, ok := mv.(string)
		if !ok {
			return fmt.Errorf("proxy hop: `module` must be a path string")
		}
		h.Module = s
		cfg := map[string]interface{}{}
		for k, v := range m {
			if k != proxyModule {
				cfg[k] = v
			}
		}
		if len(cfg) > 0 {
			h.Config = cfg
		}
		return nil
	}
	for k := range m {
		return fmt.Errorf("proxy hop: unknown key %q — a chain entry is `tls: terminate|originate` or `module: <path>`", k)
	}
	return fmt.Errorf("proxy hop: empty entry — use `tls` or `module`")
}

// The chain vocabulary: a hop is keyed by `tls` (a boundary directive) or
// `module` (a hook to load). Anything else is a typo, not a hop.
const (
	proxyTLS     = "tls"
	proxyModule  = "module"
	proxyDomains = "domains"
)

// toStringList coerces a normalized YAML/JSON value into a []string, rejecting a
// non-list or a non-string element — used for a `tls: terminate` domains list.
func toStringList(v interface{}) ([]string, error) {
	items, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("want a list, got %T", v)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("want string hostnames, got %T", it)
		}
		out = append(out, s)
	}
	return out, nil
}

// normalizeYAML rewrites yaml.v2's map[interface{}]interface{} into
// map[string]interface{} recursively, so a hop's Config is JSON-serializable
// when handed to the proxy engine. A non-string config key is an error.
func normalizeYAML(v interface{}) (interface{}, error) {
	switch x := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("config key %v is not plain text", k)
			}
			nv, err := normalizeYAML(val)
			if err != nil {
				return nil, err
			}
			m[ks] = nv
		}
		return m, nil
	case map[string]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, val := range x {
			nv, err := normalizeYAML(val)
			if err != nil {
				return nil, err
			}
			m[k] = nv
		}
		return m, nil
	case []interface{}:
		for i := range x {
			nv, err := normalizeYAML(x[i])
			if err != nil {
				return nil, err
			}
			x[i] = nv
		}
		return x, nil
	default:
		return v, nil
	}
}

// validateEgress checks a recipe's egress mode: open, none, or proxy. The union
// makes mode↔chain consistency structural (a scalar mode carries no chain), so
// this only rejects an unknown scalar mode and an empty proxy chain, then
// validates the chain itself.
func validateEgress(name string, e Egress) error {
	switch e.Mode {
	case "", EgressOpen, EgressNone:
		return nil
	case EgressProxy:
		if len(e.Proxy) == 0 {
			return fmt.Errorf("recipe %q: egress proxy needs a non-empty chain", name)
		}
		return validateProxies(name, e.Proxy)
	default:
		return fmt.Errorf("recipe %q: egress %q is not one of open, none, or {proxy: [ ... ]}", name, e.Mode)
	}
}

// validateProxies checks an egress proxy chain: a hop is a tls directive or a
// module hook. tls boundaries must nest — no terminate-inside-terminate, no
// originate without a terminate, only terminate/originate directives. A
// `tls: terminate` MUST be closed by a `tls: originate`: an unclosed window is a
// decrypt-with-no-re-encrypt, and allowing it lets a recipe express a TLS→
// plaintext downgrade. A hook that answers locally (`respond`) breaks the chain
// before the close is reached, so the close is a no-op for a pure responder — but
// it keeps the boundary explicit and forwarding always re-encrypts. A module hop
// needs NO tls window: inside one it inspects decrypted content, outside one it
// acts on the connection (host/port) for allow/deny.
func validateProxies(name string, hops []ProxyHop) error {
	terminated := false
	terminateAt := 0
	tlsWindows := 0
	for i, h := range hops {
		switch {
		case h.IsTLS():
			switch h.TLS {
			case tlsTerminate:
				if terminated {
					return fmt.Errorf("recipe %q proxy #%d: `tls: terminate` while the plaintext window is already open", name, i+1)
				}
				tlsWindows++
				if tlsWindows > 1 {
					return fmt.Errorf("recipe %q proxy #%d: only one `tls: terminate` window per chain", name, i+1)
				}
				terminated = true
				terminateAt = i + 1
			case tlsOriginate:
				if !terminated {
					return fmt.Errorf("recipe %q proxy #%d: `tls: originate` without a preceding `tls: terminate`", name, i+1)
				}
				terminated = false
			default:
				return fmt.Errorf("recipe %q proxy #%d: `tls` takes %q or %q, not %q", name, i+1, tlsTerminate, tlsOriginate, h.TLS)
			}
		case h.IsModule():
			if err := rejectControl(fmt.Sprintf("module path in recipe %q", name), h.Module); err != nil {
				return err
			}
		default:
			return fmt.Errorf("recipe %q proxy #%d: a chain entry is `tls: terminate|originate` or `module: <path>`", name, i+1)
		}
	}
	if terminated {
		return fmt.Errorf("recipe %q proxy #%d: `tls: terminate` must be closed by a `tls: originate` — an unclosed window decrypts without re-encrypting", name, terminateAt)
	}
	return nil
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
		if err := validateEgress(name, rec.Egress); err != nil {
			return err
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
