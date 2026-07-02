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

// Info is one existing sandbox instance as reported by a driver.
type Info struct {
	Name   string
	Status string
	Driver string // which sandboxing system runs it (e.g. "apple")
}

// BuildSpec describes the image a driver should build for a sandbox.
// Paths are absolute (the manifest loader resolves them).
type BuildSpec struct {
	Name       string // sandbox identity WITHIN dabs; the driver derives its own image reference
	Dockerfile string // absolute path to the build recipe
	Context    string // absolute path to the build context directory
}

// Driver is one sandboxing system. A sandbox INSTANCE is one running box
// born pristine from the image; instances are named <spec.Name>-<id> where
// id is random hex, and — like git SHAs — any unambiguous instance-name
// prefix addresses the instance in the verbs below Up.
type Driver interface {
	// Build produces the image for spec.Name's sandboxes, replacing any
	// previous build.
	Build(spec BuildSpec) error
	// Up creates and starts a NEW pristine instance from spec.Name's
	// image and returns its instance name.
	Up(spec Spec) (instance string, err error)
	// Run executes cmd inside the named instance, with the workdir and
	// env the instance was created with, streams wired to the caller.
	Run(instance string, cmd []string) error
	// Exec is Run for programs: non-interactive, combined output
	// returned instead of streamed. A non-zero exit is an error whose
	// message includes the output.
	Exec(instance string, cmd []string) (output string, err error)
	// Down stops and removes the named instance. Removing an absent
	// instance is not an error.
	Down(instance string) error
	// Ls lists the instances this driver manages.
	Ls() ([]Info, error)
}
