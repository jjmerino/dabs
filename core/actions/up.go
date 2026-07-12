package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/recipe"
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
	// Cut the PLACE first: a box names its parent's spaces ($PARENT_VOLUME), and a
	// parent must exist to be named.
	_, tip, hosts, kept, cut, err := r.provisionPlaces(name, rec.Sources, "")
	if err != nil {
		return err
	}
	boxID, vars, err := r.mintBoxNode(name, tip)
	if err != nil {
		return err
	}
	// Validate sources before any side effect, then resolve the image WITHOUT
	// building the recipe's own Dockerfile: `up` boots an image a prior
	// `dabs build` produced (it may run where no builder exists).
	resolved, err := r.validateSources(name, rec.Sources, vars, hosts)
	if err != nil {
		return err
	}
	image, err := r.resolveBuiltImage(drv, name, rec.Image, rec.Target)
	if err != nil {
		return err
	}
	instance, err := r.buildBox(drv, name, boxID, tip, rec, image, rec.Sources, resolved, cut)
	if err != nil {
		return err
	}
	// `up` is DETACHED: it never runs the recipe's command and never tears the
	// box down — keep is implicit. The box is the user's to reap with `dabs down`.
	for _, k := range kept {
		fmt.Fprintln(os.Stdout, tui.Success("kept: %s", k))
	}
	printUp(name, instance, rec)
	return nil
}

// printUp reports what `up` did and what to do next. The instance is named after
// the IMAGE, so it alone never says which recipe booted the box; and `up`
// deliberately runs no command, which users assume it did. Both facts, plus the
// three commands that follow (reap, shell in, run what the recipe encodes), are
// spelled out here rather than left for the reader to reconstruct.
func printUp(name, instance string, rec recipe.Recipe) {
	head := fmt.Sprintf("recipe up: %s", tui.Accent(name))
	if rec.Target != "" {
		head += fmt.Sprintf(" (on %s)", rec.Target)
	}
	fmt.Fprintln(os.Stdout, tui.Success("%s", head))
	fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("id:"), tui.Accent(instance))
	fmt.Fprintln(os.Stdout, tui.Muted("(no command was run — the recipe's command is not started by `up`)"))
	fmt.Fprintf(os.Stdout, "%s dabs down %s\n", tui.Muted("bring down:"), instance)
	fmt.Fprintf(os.Stdout, "%s dabs exec %s -- sh\n", tui.Muted("sh in:"), instance)
	if len(rec.Command) == 0 {
		fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("run recipe command:"), tui.Muted("(this recipe declares no command)"))
		return
	}
	// There is no verb that runs the recipe's own command in a box that is
	// already up — `dabs recipe` boots a NEW box. So print the argv itself,
	// runnable as-is through exec.
	fmt.Fprintf(os.Stdout, "%s dabs exec %s -- %s\n", tui.Muted("run recipe command:"), instance, quoteArgv(rec.Command))
}

// quoteArgv renders an argv as a copy-pasteable shell command line: any argument
// that is not plainly safe is single-quoted, so a `sh -c "a && b"` command line
// survives the round trip through the user's shell into `dabs exec`.
func quoteArgv(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		}) < 0 {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}
