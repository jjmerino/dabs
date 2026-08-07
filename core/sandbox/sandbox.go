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

// Egress modes a Spec may request. Open is the default; None cuts all outbound
// network; Proxy makes a host proxy the box's only way out.
const (
	EgressOpen  = "open"
	EgressNone  = "none"
	EgressProxy = "proxy"
)

// Spec describes the sandbox a driver should provide. It is vendor-neutral:
// drivers translate it into their own vocabulary.
type Spec struct {
	Name    string            // sandbox identity WITHIN dabs; the actual driver image name may vary vendor to vendor
	Workdir string            // working directory inside the sandbox
	Env     map[string]string // environment inside the sandbox
	Mounts  []Mount           // live host paths attached into the box
	// Sockets are host unix sockets exposed inside the box, each at its own Path.
	// The caller has resolved every host path and confirmed it names a socket; a
	// driver attaches it exactly. A socket is the box's line to a program that is
	// already listening on the host, so it is attached whatever the egress mode.
	Sockets []Mount
	// Egress is the box's outbound network: "" or EgressOpen (unrestricted),
	// EgressNone, or EgressProxy. The caller has already confirmed the driver
	// enforces it (EgressEnforcer); a driver never degrades a mode it was given.
	// A proxy box's way out rides its door, which the caller hands the driver in
	// Sockets like any other; there is nothing egress-specific to mount.
	Egress string
	// ForwarderBin is the host path of the single-purpose forwarder binary to
	// mount into the box at forwarder.ForwardPath for EgressProxy. The caller
	// materializes it (from dabs's embedded copy); a driver mounts it exactly.
	// Empty otherwise, and unused by drivers whose image carries the forwarder
	// (apple's micro-VM).
	ForwarderBin string
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

// EgressEnforcer is an OPTIONAL driver capability: a driver that can restrict
// a box's outbound network implements it and states, mechanically, whether it
// enforces a given mode — and when it cannot, why (a mechanical fact about
// this driver: the platform, the binary, the transport). The policy sits
// above: actions asks BEFORE Up and refuses to boot a box whose requested
// egress the driver cannot enforce, so a mode is never silently degraded to
// open. A driver without the interface enforces only open.
type EgressEnforcer interface {
	// CheckEgress reports whether this driver enforces the given egress mode
	// (nil) or why it cannot (an error stating the mechanical reason).
	CheckEgress(mode string) error
}

// DetachedLogDir is where the box sees the directory a detached command's log is
// written into, in dabs's own box-path namespace. The caller binds the box
// node's own tmp space here, so the log is a plain host file that lives and dies
// with the node it belongs to.
const DetachedLogDir = "/run/dabs/log"

// DetachedLogName is the file, in DetachedLogDir and in the node's tmp space,
// that a detached command's output is written to. Stable by contract: it is what
// a caller tails.
const DetachedLogName = "detached.log"

// DetachedLogPath is the full box path a detached command's combined output is
// redirected to. Both streams share the one file, interleaved as they happen —
// that is the order the command produced them in, and splitting them would hide
// which output preceded which error.
const DetachedLogPath = DetachedLogDir + "/" + DetachedLogName

// Detacher is an OPTIONAL driver capability: a driver that has an ANSWER about
// running a command in the background — either it can (its box carries a
// long-lived process of its own) or it states, mechanically, why it cannot. The
// policy sits above: actions asks CheckDetach BEFORE Up and refuses to boot a
// box whose command the driver cannot hold, so `--detach` is never degraded
// into a foreground run.
//
// A driver that answers "no" still implements this interface, so the reason
// lives with the driver that owns it and no caller has to guess one. A driver
// that implements nothing at all gets the caller's plain refusal, which claims
// no cause it cannot know.
type Detacher interface {
	// CheckDetach reports whether this driver can hold a detached command (nil)
	// or why it cannot (an error stating the mechanical reason: the platform,
	// the transport, how the driver enters its box).
	CheckDetach() error
	// Detach starts cmd inside the instance with the workdir and env the
	// instance was created with, its combined output redirected to
	// DetachedLogPath inside the box, and returns as soon as the command is
	// started — never waiting for it to exit, never wiring it to the caller's
	// streams. The caller has bound a host directory at DetachedLogDir, so what
	// the command writes there outlives the box's own filesystem. It needs a
	// shell in the box (the redirect is the box's own). A driver whose
	// CheckDetach refuses returns that same refusal here.
	Detach(instance string, cmd []string) error
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

// Capable is Driver plus EVERY optional capability, in one list. It exists for
// wrappers: a decorator that stands in front of a driver (see Lazy) must answer
// for all of them, because a caller reaches the capability by type-asserting the
// WRAPPER — a forward the wrapper forgets does not fail, it silently reports
// "this driver cannot", naming a driver that can. Pinning each wrapper to this
// interface turns that into a compile error, and adding a capability here is
// what makes the compiler go find every wrapper.
//
// Drivers themselves do NOT implement Capable; picking which capabilities to
// offer is the whole point of an optional one.
type Capable interface {
	Driver
	EgressEnforcer
	ImageStore
	Detacher
}
