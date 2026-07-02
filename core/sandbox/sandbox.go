// Package sandbox is the contract between dabs core logic and the sandboxing
// systems that implement it (Apple container, cloud providers, …). Contract
// and shared types ONLY — zero vendor imports, zero logic. Implementations
// live in subpackages (sandbox/apple, …) and are injected at the composition
// root; OS-coupled ones are build-tagged so they never ship in a foreign
// binary.
package sandbox

// Spec describes the sandbox a driver should provide. It is vendor-neutral:
// drivers translate it into their own vocabulary.
type Spec struct {
	Name    string            // sandbox identity WITHIN dabs; the actual driver image name may vary vendor to vendor
	Workdir string            // working directory inside the sandbox
	Env     map[string]string // environment inside the sandbox
}

// Info is one existing sandbox as reported by a driver.
type Info struct {
	Name   string
	Status string
}

// BuildSpec describes the image a driver should build for a sandbox.
// Paths are absolute (the manifest loader resolves them).
type BuildSpec struct {
	Name       string // sandbox identity WITHIN dabs; the driver derives its own image reference
	Dockerfile string // absolute path to the build recipe
	Context    string // absolute path to the build context directory
}

// Driver is one sandboxing system.
type Driver interface {
	// Build produces the sandbox's image from spec, replacing any
	// previous build.
	Build(spec BuildSpec) error
	// Up ensures the sandbox is running. fresh recreates it first,
	// restoring the image's pristine state.
	Up(spec Spec, fresh bool) error
	// Down stops and removes the sandbox. Removing an absent sandbox is
	// not an error.
	Down(name string) error
	// Ls lists the sandboxes this driver manages.
	Ls() ([]Info, error)
}
