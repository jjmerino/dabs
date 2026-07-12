package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Up resolves a recipe (no name → the registry default, a name → that recipe, a
// path → a dabs.yaml to load), prepares its sources, and starts a NEW pristine
// DETACHED instance on the recipe's target (local by default): image, sources,
// env, and workdir. Unlike `dabs recipe`, it does NOT run the recipe's command
// and does NOT tear the box down — it reports the instance name and leaves the
// box up for `dabs exec`/`dabs run` (and `dabs down` to reap).
func (r Real) Up(p params.Up) error {
	reg, name, err := r.resolveRecipe(p.Name)
	if err != nil {
		return err
	}
	rec, err := reg.Get(name)
	if err != nil {
		return err
	}
	drv, err := r.driverFor(rec.Target)
	if err != nil {
		return err
	}
	// Name the box's node first: a source may mount one of its spaces, so the
	// paths must exist as values before anything is validated or built.
	boxID, vars, err := r.mintBoxNode(name)
	if err != nil {
		return err
	}
	// Validate sources before any side effect, then resolve the image WITHOUT
	// building the recipe's own Dockerfile: `up` boots an image a prior
	// `dabs build` produced (it may run where no builder exists).
	resolved, err := r.validateSources(name, rec.Sources, vars)
	if err != nil {
		return err
	}
	image, err := r.resolveBuiltImage(drv, name, rec.Image, rec.Target)
	if err != nil {
		return err
	}
	instance, kept, err := r.buildBox(drv, name, boxID, rec, image, rec.Sources, resolved)
	if err != nil {
		return err
	}
	// `up` is DETACHED: it never runs the recipe's command and never tears the
	// box down — keep is implicit. The box is the user's to reap with `dabs down`.
	for _, k := range kept {
		fmt.Fprintln(os.Stdout, tui.Success("worktree kept: %s", k))
	}
	if rec.Target != "" {
		fmt.Fprintln(os.Stdout, tui.Success("%s up on %s", tui.Accent(instance), rec.Target))
		return nil
	}
	fmt.Fprintln(os.Stdout, tui.Success("%s up", tui.Accent(instance)))
	return nil
}
