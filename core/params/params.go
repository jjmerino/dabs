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

// Up are the inputs to the up action. Name selects the recipe to bring up, with
// the same meaning as Build.Name.
type Up struct {
	Name string // recipe name, a dabs.yaml path, or "" for the default
}

// Exec are the inputs to the exec action: the lowest level — run an EXACT argv
// inside an instance, with no shell interpretation.
type Exec struct {
	Instance string   // instance name, as reported by ls (e.g. demo-0)
	Cmd      []string // exact argv, run as-is
}

// Run are the inputs to the run action: the friendly level — run a shell
// command LINE inside an instance. Cmd's tokens are joined into one `sh -c`
// command, so pipes/globs/&& work as written.
type Run struct {
	Instance string   // instance name, as reported by ls (e.g. demo-0)
	Cmd      []string // tokens joined into one shell command line
}

// Down are the inputs to the down action.
type Down struct {
	Instance string // instance name, as reported by ls (e.g. demo-0)
	Force    bool   // skip the confirmation prompt / force through
	Multiple bool   // authorize acting on more than one match; without it a name matching several instances is refused
	Dry      bool   // only show what the name matches; down nothing
}

// Ls are the inputs to the ls action.
type Ls struct{}

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

// Auth are the inputs to the auth action: log a harness into a persistent host
// vault so future sandboxes mount it and start already-authenticated.
type Auth struct {
	Provider string // "claude"
}

// Recipe are the inputs to running a named recipe (a fully declarative box:
// image, sources, env, command).
type Recipe struct {
	Name string
	// Worktree, when set (via `dabs cast`), binds an EXISTING dabs worktree to
	// the recipe's `.` source instead of the cwd: `worktree: .`/`mount: .` mount
	// that worktree live (plus its parent .git so git works in-box) rather than
	// cutting a fresh branch, and `copy: .` snapshots it.
	Worktree string
	// Cmd, when non-empty, is APPENDED to the recipe's own command (e.g.
	// `dabs recipe claude --model x` → `claude --model x`). Passing a command
	// triggers a look-before-run confirmation.
	Cmd []string
}

// Do are the inputs to `dabs do`: run a command in a throwaway recipe box. It
// is an alias for the default recipe (the dabs.yaml `default:`, else the
// bundled `sh` box), with Cmd appended to that recipe's command.
type Do struct {
	Cmd []string // command appended to the resolved recipe's command
}

// Recipes are the inputs to listing the known recipes.
type Recipes struct {
	Print bool // print the full bundled recipes YAML (the authoring format) instead of a summary
}

// Worktrees are the inputs to inspecting/reaping recipe-created git worktrees.
type Worktrees struct {
	Sub   string // "" | "ls" | "diff" | "rm" | "prune"
	Name  string // for diff/rm
	Force bool   // approve discarding a worktree that holds unreviewed work
}

// Actions is the contract every action provider satisfies: the real
// implementations in core/actions, fakes in tests, RPC clients later.
type Actions interface {
	Build(Build) error
	Up(Up) error
	Auth(Auth) error
	Recipe(Recipe) error
	Do(Do) error
	Recipes(Recipes) error
	Worktrees(Worktrees) error
	Exec(Exec) error
	Run(Run) error
	Down(Down) error
	Ls(Ls) error
	ServersList(ServersList) error
	ServersAdd(ServersAdd) error
	ServersRemove(ServersRemove) error
}
