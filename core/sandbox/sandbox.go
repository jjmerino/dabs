// Package sandbox is the contract between dabs core logic and the sandboxing
// systems that implement it (Apple container, cloud providers, …). Contract
// and shared types ONLY — zero vendor imports, zero logic. Implementations
// live in subpackages (sandbox/apple, …) and are injected at the composition
// root; OS-coupled ones are build-tagged so they never ship in a foreign
// binary.
//
// Drivers are MECHANICAL: they take EXACT instance names and expose what
// exists. All policy — abbreviation/prefix resolution, ambiguity handling,
// force/dry semantics — is dabs domain logic and lives in core/actions,
// which resolves against Ls and then addresses the driver exactly.
package sandbox

import "errors"

// ErrNoBuilder marks a Build refusal because the host carries no image builder
// (bwrap builds with docker). A driver wraps it so a caller can tell "cannot
// build HERE" from a failed build — and serve an already-present image instead
// of failing a boot that never needed a build.
var ErrNoBuilder = errors.New("no image builder on this host")

// Mount is a live host directory (or file) attached into a box at Path.
// Unlike image layers, a mount is read-write-through by default: writes inside
// the box land on the host and persist past the box. Drivers that cannot mount
// ignore it; the apple driver honors it.
type Mount struct {
	Host string // absolute host path (the source of truth, outlives the box)
	Path string // absolute path inside the box
	RO   bool   // mount read-only (box can read but not write back)
}

// Spec describes the sandbox a driver should provide. It is vendor-neutral:
// drivers translate it into their own vocabulary.
type Spec struct {
	Name    string            // sandbox identity WITHIN dabs; the actual driver image name may vary vendor to vendor
	Workdir string            // working directory inside the sandbox
	Env     map[string]string // environment inside the sandbox
	Mounts  []Mount           // live host paths attached into the box
}

// Info is one existing sandbox instance as reported by a driver.
type Info struct {
	Name   string
	Status string
	Driver string // which sandboxing system runs it (e.g. "apple")
}

// BuildSpec describes the image a driver should build for a sandbox.
// Paths are absolute (the recipe/image resolver resolves them).
type BuildSpec struct {
	Name       string // sandbox identity WITHIN dabs; the driver derives its own image reference
	Dockerfile string // absolute path to the build recipe
	Context    string // absolute path to the build context directory
}

// Driver is one sandboxing system. A sandbox INSTANCE is one running box
// born pristine from the image, named <spec.Name>-<id> with a random hex id.
// Every instance parameter below is an EXACT name from Ls.
type Driver interface {
	// Build produces the image for spec.Name's sandboxes, replacing any
	// previous build.
	Build(spec BuildSpec) error
	// HasImage reports whether an image for name has already been built, so a
	// caller can skip a redundant Build. A driver that cannot cheaply tell
	// returns false (the caller then builds, which is safe and idempotent).
	HasImage(name string) (bool, error)
	// Up creates and starts a NEW pristine instance from spec.Name's
	// image and returns its instance name.
	Up(spec Spec) (instance string, err error)
	// Run executes cmd inside the instance, with the workdir and env the
	// instance was created with, streams wired to the caller.
	Run(instance string, cmd []string) error
	// Exec is Run for programs: non-interactive, combined output
	// returned instead of streamed. A non-zero exit is an error whose
	// message includes the output.
	Exec(instance string, cmd []string) (output string, err error)
	// Down stops and removes the instance. Removing an absent instance
	// is not an error.
	Down(instance string) error
	// Ls lists the instances this driver manages.
	Ls() ([]Info, error)
	// Kind is the driver's identity ("apple", "bwrap", "ssh", …) — the
	// same tag it stamps on Info.Driver, reachable without any instances.
	Kind() string
}

// Image is one built image in a driver's local store: its name (the recipe
// image name, without any driver-internal prefix) and size in bytes (0 when the
// driver cannot report it cheaply).
type Image struct {
	Name string
	Size int64
}

// ImageStore is an OPTIONAL driver capability: a driver that keeps a reapable
// local image store implements it so `dabs prune --dry` can list what a build
// left behind and `dabs prune` can reclaim it. A driver without a local store
// (e.g. a remote server) simply does not implement it, and the action skips it.
type ImageStore interface {
	// Images lists the images this driver has built and still holds.
	Images() ([]Image, error)
	// RemoveImage deletes one image by the name Images reported. Removing an
	// absent image is not an error.
	RemoveImage(name string) error
}
