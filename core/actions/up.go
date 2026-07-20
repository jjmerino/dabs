package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/proxy"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/tui"
)

// upDetached backs `dabs recipe --detach`: it resolves a recipe (no arg → the
// registry default, a name → that recipe, a path → a dabs.yaml to load),
// prepares its sources, and starts a NEW pristine DETACHED instance on the
// recipe's target (local by default): image, sources, env, and workdir. Unlike
// a plain `dabs recipe`, it does NOT run the recipe's command and does NOT tear
// the box down — it reports the instance name and leaves the box up for
// `dabs exec` (and `dabs rm` to reap). worktree, when set, binds an EXISTING dabs
// worktree to the recipe's `.` source (mounting its parent .git so git works
// in-box) instead of the cwd — the `--detach` form of `dabs recipe --worktree`.
func (r Real) upDetached(arg, worktree, nodeName string) error {
	reg, name, err := r.resolveRecipe(arg)
	if err != nil {
		return err
	}
	rec, err := reg.Get(name)
	if err != nil {
		return err
	}
	boxless := rec.Image.Name == "" && rec.Image.Dockerfile == ""
	if err := r.checkSources(name, rec.Sources, boxless); err != nil {
		return err
	}
	// A recipe with no image is a recipe for a PLACE, not a box. `--detach` on one
	// provisions its nodes and stops — the same outcome as a plain `dabs recipe`,
	// so the two paths agree instead of `--detach` erroring on a boxless recipe.
	if boxless {
		return r.provisionNodes(name, rec, worktree, nodeName)
	}
	// Booting a box from inside a dabs worktree's own checkout parents the box on
	// that worktree, exactly as an explicit --worktree would (which wins).
	if worktree == "" {
		owner, oerr := r.resolveOwningWorktree()
		if oerr != nil {
			return oerr
		}
		worktree = owner
	}
	// `--worktree <wt>` binds an existing worktree to the `.` source (mounting its
	// parent .git so git works in-box) instead of cutting a fresh place.
	sources := rec.Sources
	if worktree != "" {
		full, werr := r.resolveWorktreeArg(worktree)
		if werr != nil {
			return werr
		}
		worktree = full
		sources, err = r.bindWorktree(name, rec.Sources, worktree)
		if err != nil {
			return err
		}
	}
	drv, err := r.driverFor(rec.Target)
	if err != nil {
		return err
	}
	// The image is resolved first — WITHOUT building the recipe's own
	// Dockerfile: `--detach` boots an image a prior `dabs build` produced (it
	// may run where no builder exists) — and the claim runs after it and every
	// other name-independent refusal, so a boot refused for those reasons has
	// not touched the name's holder (provisionNodes claims for the boxless
	// path above).
	image, err := r.resolveBuiltImage(drv, name, rec.Image, rec.Target)
	if err != nil {
		return err
	}
	if nodeName != "" {
		if err := r.claimNodeName(nodeName); err != nil {
			return err
		}
	}
	// Cut the PLACE first: a box names its parent's spaces ($PARENT_VOLUME), and a
	// parent must exist to be named.
	_, tip, hosts, kept, cut, err := r.provisionPlaces(name, snapshotRecipe(rec), sources, worktree)
	if err != nil {
		return err
	}
	boxID, vars, err := r.mintBoxNode(name, tip, nodeName)
	if err != nil {
		return err
	}
	resolved, err := r.validateSources(name, sources, vars, hosts)
	if err != nil {
		return err
	}
	instance, err := r.buildBox(drv, name, boxID, tip, rec, image, sources, resolved, cut)
	if err != nil {
		return err
	}
	// A box that cannot be ENTERED is not up: a source mounted over `/`, a
	// `workdir:` missing from the image, or a read-only parent masking an rw child
	// all let Up report success while every later exec fails `bwrap: Can't chdir`.
	// Enter once with a no-op; if that fails the boot did not really succeed —
	// reap the box so no unusable instance lingers and surface the driver's message.
	if _, serr := drv.Exec(instance, []string{"true"}); serr != nil {
		proxy.Reap(r.boxProxy(instance)) // the box is abandoned; reap its engine too, not just the box
		_ = drv.Down(instance)
		return fmt.Errorf("boot failed: box is not usable: %w", serr)
	}
	// A bound worktree is mounted, not cut, so buildBox never journals it — record
	// its box→worktree link here so `worktrees ls` shows the box as live.
	if worktree != "" {
		if data, derr := r.resolveNodeData(worktree); derr == nil {
			r.logWorktreeUp(instance, worktree, data, name)
		}
	}
	// `--detach` is DETACHED: it never runs the recipe's command and never tears
	// the box down — keep is implicit. The box is the user's to reap with `dabs rm`.
	for _, k := range kept {
		fmt.Fprintln(os.Stdout, tui.Success("kept: %s", k))
	}
	printUp(name, boxID, instance, rec)
	return nil
}

// printUp reports what `--detach` did and what to do next. The box has two names:
// its NODE ID — the canonical, stable handle rm/exec resolve first — and the
// driver's INSTANCE name, minted after the box comes up and named after the
// IMAGE. The handle shown is the node id; the instance is kept on its own line so
// the mapping is not lost. The instance alone never says which recipe booted the
// box, and `--detach` deliberately runs no command (users assume it did) — both
// facts, plus the three commands that follow (reap, shell in, run what the recipe
// encodes), are spelled out here rather than left for the reader to reconstruct.
func printUp(name, nodeID, instance string, rec recipe.Recipe) {
	head := fmt.Sprintf("recipe booted: %s", tui.Accent(name))
	if rec.Target != "" {
		head += fmt.Sprintf(" (on %s)", rec.Target)
	}
	fmt.Fprintln(os.Stdout, tui.Success("%s", head))
	fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("id:"), tui.Accent(nodeID))
	fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("instance:"), instance)
	fmt.Fprintln(os.Stdout, tui.Muted("(no command was run — the recipe's command is not started by `--no-command`)"))
	fmt.Fprintf(os.Stdout, "%s dabs rm %s\n", tui.Muted("reap:"), nodeID)
	// The "sh in:" line runs `dabs exec <id> -- sh`. When the recipe's own
	// command IS exactly `sh`, the "run recipe command:" line below renders the
	// identical argv, so printing both would repeat one command under two labels —
	// drop the "sh in:" line and let the recipe-command line stand for both.
	if len(rec.Command) != 1 || rec.Command[0] != "sh" {
		fmt.Fprintf(os.Stdout, "%s dabs exec %s -- sh\n", tui.Muted("sh in:"), nodeID)
	}
	if len(rec.Command) == 0 {
		fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("run recipe command:"), tui.Muted("(this recipe declares no command)"))
		return
	}
	// There is no verb that runs the recipe's own command in a box that is
	// already up — `dabs recipe` boots a NEW box. So print the argv itself,
	// runnable as-is through exec.
	fmt.Fprintf(os.Stdout, "%s dabs exec %s -- %s\n", tui.Muted("run recipe command:"), nodeID, quoteArgv(rec.Command))
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
