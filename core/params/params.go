// Package params.
//
// A leaf contract package: the typed params object for every action, plus
// the Actions interface they collectively form. It has no
// dependencies and no logic — cli, core/actions, and future RPC transports
// all meet here.
//
// Litmus test for AGENTS: if your edit makes it so that this file
// cannot be converted to a .proto file — logic, dependencies, non-serializable
// fields (funcs, channels, io types) — then it does not go in this file.
package params

// Build are the inputs to the build action. Name selects the recipe to build:
// "" is the registry default, a bare name is that recipe, and a path is a
// dabs.yaml (or a dir containing one) to load and take the default from.
type Build struct {
	Name string // recipe name, a dabs.yaml path, or "" for the default
}

// Exec are the inputs to the exec action, the single reach-in verb. The `--`
// separator on the command line selects the mode: with `--`, Cmd is an EXACT
// argv run as-is (Shell false); without it, Cmd's tokens are joined into one
// `sh -c` line so pipes/globs/&& work (Shell true).
type Exec struct {
	Instance string   // instance name, as reported by ls (e.g. demo-0)
	Cmd      []string // exact argv, or shell tokens joined into one line
	Shell    bool     // join Cmd into one `sh -c` line instead of running it as-is
}

// Ls are the inputs to the ls action.
type Ls struct {
	// All also lists ARCHIVED nodes — boxes no driver holds any more. They are
	// kept as the record of what ran and from where; `ls` hides them because what
	// you almost always want to know is what is live.
	All bool
}

// Rm are the inputs to removing a node: a place dabs made, or a box. It is the
// single reaper — it stops a live box and takes its node and spaces away.
//
// Yes skips the consent prompt: it reaps the ephemeral space (the one that may
// hold work) and stops a live box without asking. Without it, a reap that would
// stop a live box or lose held data is REFUSED with a preview.
// Keep archives instead of removing: the box is stopped but its node record is
// left behind (what ran, and from where, outlives the box). This is teardown
// without forgetting.
// Volume additionally consents to the volume — what a place keeps ON PURPOSE,
// so it is never taken without being asked for by name.
// Force approves discarding a worktree node that holds unreviewed git work
// (uncommitted changes or unpushed commits) — a different risk than the prompt
// Yes skips, so it stays its own flag.
// Multiple authorizes acting on more than one prefix match; without it a name
// matching several nodes is refused, so a stray prefix cannot reap several
// nodes at once — the count is shown first.
// Dry previews what would be reaped and removes nothing.
// CleanWorktrees reaps EVERY worktree node that holds no unreviewed work, in one
// sweep, instead of a single named node. A worktree with unreviewed work is kept
// unless Force. When set, Node is not required.
type Rm struct {
	Node           string
	Yes            bool
	Keep           bool
	Volume         bool
	Force          bool
	Multiple       bool
	Dry            bool
	CleanWorktrees bool
}

// Prune are the inputs to the prune action: reclaim built box images (they
// rebuild on the next build). Dry lists what exists instead of removing it;
// Force removes even an image a live box still depends on.
type Prune struct {
	Dry   bool
	Force bool
}

// ServersList are the inputs to listing registered servers.
type ServersList struct{}

// ServersAdd are the inputs to registering a server: a remote machine with
// dabs installed. Via names the transport (default "ssh"); Host is that
// transport's address.
type ServersAdd struct {
	Name string // fleet name (what a recipe's "target" routes to)
	Host string // transport address; defaults to Name
	Via  string // transport strategy; default "ssh"
}

// ServersRemove are the inputs to unregistering a server.
type ServersRemove struct {
	Name string
}

// Recipe are the inputs to running a recipe (a fully declarative box: image,
// sources, env, command).
type Recipe struct {
	// Name is an already-chosen recipe run directly. The `dabs recipe` verb leaves
	// it empty and passes Args instead, letting the action decide name-vs-default
	// against the registry.
	Name string
	// Worktree, when set (via `dabs recipe <name> --worktree <wt>`), binds an
	// EXISTING dabs worktree to the recipe's `.` source instead of the cwd:
	// `worktree: .`/`mount: .` mount that worktree live (plus its parent .git so
	// git works in-box) rather than cutting a fresh branch, and `copy: .` snapshots
	// it. Composes with Detach.
	Worktree string
	// Args are the positional tokens of `dabs recipe [name] [cmd…]`. If the first
	// is a KNOWN recipe, it is the recipe and the rest are appended to its
	// command; otherwise (or with no args) the registry DEFAULT recipe runs (the
	// dabs.yaml `default:`, else the bundled `sh` box) with ALL of Args appended.
	Args []string
	// Default forces the default-recipe path (a leading `--`), so a command whose
	// first token happens to be a recipe name still runs against the default.
	Default bool
	// Detach boots a NEW pristine DETACHED box from the recipe and does NOT run
	// the recipe's command — it reports the instance and leaves the box up for
	// `dabs exec` (and `dabs rm` to reap). Args[0], when present, is the recipe
	// name or a dabs.yaml path; no command is appended in this mode.
	Detach bool
}

// Recipes are the inputs to listing the known recipes.
type Recipes struct {
	Print bool // print the full bundled recipes YAML (the authoring format) instead of a summary
}

// Worktrees are the inputs to inspecting/reaping recipe-created git worktrees.
type Worktrees struct {
	Sub  string // "" | "ls" | "diff"
	Name string // for diff
}

// Actions is the contract every action provider satisfies: the real
// implementations in core/actions, fakes in tests, RPC clients later.
type Actions interface {
	Build(Build) error
	Recipe(Recipe) error
	Recipes(Recipes) error
	Worktrees(Worktrees) error
	Exec(Exec) error
	Ls(Ls) error
	Rm(Rm) error
	Prune(Prune) error
	ServersList(ServersList) error
	ServersAdd(ServersAdd) error
	ServersRemove(ServersRemove) error
}
