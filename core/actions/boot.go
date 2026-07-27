package actions

import (
	"fmt"

	"github.com/jjmerino/dabs/core/recipe"
)

// Box is a booted box's identity, as handed back to a Go caller: the node ID —
// the canonical, stable handle `exec`/`rm`/`cd` resolve first — and the driver's
// INSTANCE name, minted after the box comes up and named after the image. Both
// resolve everywhere a name resolves; the node ID is the one to keep.
type Box struct {
	ID       string // node id (the recipe name plus a minted suffix, or BootSpec.NodeName)
	Instance string // driver instance name, as reported by ls
}

// BootSpec is the input to Boot: a recipe VALUE plus the knobs `dabs recipe
// --detach` takes on the command line.
type BootSpec struct {
	// Name labels the boot: it prefixes the box's node id, tags a Dockerfile-backed
	// image, and appears in error messages. It is not looked up anywhere — the
	// recipe below is the whole spec — but it is required, because a node id and an
	// image tag both need one. It takes an id's own shape (letters, digits, dots,
	// underscores, dashes, starting alphanumeric), because it becomes a directory
	// name under ~/.dabs/nodes and an image tag.
	Name string
	// Recipe is the box, whole: image, sources, env, workdir, target, egress. It
	// must declare an image; a recipe with none provisions a PLACE, not a box, and
	// there would be nothing to return.
	Recipe recipe.Recipe
	// Worktree, when set, binds an EXISTING dabs worktree to the recipe's `.`
	// source (mounting its parent .git so git works in-box) instead of the cwd —
	// what `dabs recipe --worktree <wt>` does.
	Worktree string
	// NodeName is the id the box node gets instead of a minted one — what `--name`
	// does. It must be unique across known nodes; an INACTIVE holder is reaped on
	// the fly, an active one refuses the boot.
	NodeName string
}

// Boot brings a NEW pristine DETACHED box up from a recipe VALUE and returns its
// identity. It is the Go entry point behind `dabs recipe --detach`, for callers
// that embed dabs rather than shell out to it: the recipe never touches disk and
// needs no registry entry, nothing is printed, and the box's node id and instance
// come back as values to drive with Exec and reap with Rm.
//
// Like `--detach`, it does NOT run the recipe's command and does NOT tear the box
// down — the box is the caller's to reap.
//
// An error returns the zero Box, and a boot that died after minting its node
// leaves that node behind. Set NodeName to have a handle to reap it by: a minted
// id is never reported on the failure path.
func (r Real) Boot(spec BootSpec) (Box, error) {
	if spec.Name == "" {
		return Box{}, fmt.Errorf("boot: a name is required — it prefixes the box's node id and tags its image")
	}
	// The name becomes a node id and a directory under ~/.dabs/nodes. On the CLI
	// path it is a registry key the parser already vetted; here the caller supplies
	// it raw, so it is checked against the id shape before it can name a path.
	if err := validateIDShape("boot name", spec.Name); err != nil {
		return Box{}, err
	}
	rec := spec.Recipe
	if rec.Image.Name == "" && rec.Image.Dockerfile == "" {
		return Box{}, fmt.Errorf("recipe %q: no image, so there is no box to boot", spec.Name)
	}
	// The recipe never went through recipe.Parse, so run the post-parse gate over
	// the value itself: a colliding box path, a control byte, or a malformed egress
	// spec refuses here exactly as it would in a dabs.yaml.
	if err := recipe.Validate(spec.Name, rec); err != nil {
		return Box{}, err
	}
	if err := r.checkSources(spec.Name, rec.Sources, false); err != nil {
		return Box{}, err
	}
	box, _, err := r.bootDetached(spec.Name, rec, spec.Worktree, spec.NodeName)
	return box, err
}
