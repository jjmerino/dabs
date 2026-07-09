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

// Build are the inputs to the build action.
type Build struct {
	ManifestPath string // path to manifest file or dir containing one
}

// Up are the inputs to the up action.
type Up struct {
	ManifestPath string // path to manifest file or dir containing one
}

// Run are the inputs to the run action.
type Run struct {
	Instance string   // instance name, as reported by ls (e.g. demo-0)
	Cmd      []string // command to execute inside the instance
}

// Down are the inputs to the down action.
type Down struct {
	Instance string // instance name, as reported by ls (e.g. demo-0)
	Force    bool   // when the name matches several instances, down them all
	Dry      bool   // only show what the name matches; down nothing
}

// Ls are the inputs to the ls action.
type Ls struct{}

// Mcp are the inputs to the mcp action: serve the dabash MCP tool over
// stdio, curried to one instance — the tool takes no sandbox parameter.
type Mcp struct {
	Instance string // instance name, as reported by ls (e.g. demo-0)
}

// ServersList are the inputs to listing registered servers.
type ServersList struct{}

// ServersAdd are the inputs to registering a server: a remote machine with
// dabs installed. Via names the transport (default "ssh"); Host is that
// transport's address.
type ServersAdd struct {
	Name string // fleet name (what manifests put in "target")
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

// Install are the inputs to installing a harness integration. Empty Harness
// prints instructions.
type Install struct {
	Harness string // "pi" | "claude" | ""
}

// Uninstall are the inputs to removing a harness integration.
type Uninstall struct {
	Harness string // "pi" | "claude"
}

// Actions is the contract every action provider satisfies: the real
// implementations in core/actions, fakes in tests, RPC clients later.
type Actions interface {
	Build(Build) error
	Up(Up) error
	Auth(Auth) error
	Run(Run) error
	Down(Down) error
	Ls(Ls) error
	Mcp(Mcp) error
	ServersList(ServersList) error
	ServersAdd(ServersAdd) error
	ServersRemove(ServersRemove) error
	Install(Install) error
	Uninstall(Uninstall) error
}
