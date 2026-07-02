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

// Up are the inputs to the up action.
type Up struct {
	Manifest string // path to manifest file or dir containing one
	Fresh    bool   // recreate the container == pristine state
}

// Down are the inputs to the down action.
type Down struct {
	Manifest string // path to manifest file or dir containing one
}

// Ls are the inputs to the ls action.
type Ls struct{}

// Actions is the contract every action provider satisfies: the real
// implementations in core/actions, fakes in tests, RPC clients later.
type Actions interface {
	Up(Up) error
	Down(Down) error
	Ls(Ls) error
}
