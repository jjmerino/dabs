package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjmerino/dabs/core/proxy"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// upDetached backs `dabs recipe --no-command` and `dabs recipe --detach`: it
// resolves a recipe (no arg → the registry default, a name → that recipe, a
// path → a dabs.yaml to load), prepares its sources, and starts a NEW pristine
// instance on the recipe's target (local by default): image, sources, env, and
// workdir. Unlike a plain `dabs recipe` it does NOT tear the box down — it
// reports the instance name and leaves the box up for `dabs exec` (and
// `dabs rm` to reap). startCommand picks which of the two it is: false runs
// nothing, true starts the recipe's own command in the BACKGROUND inside the
// box and returns without waiting for it. worktree, when set, binds an EXISTING
// dabs worktree to the recipe's `.` source (mounting its parent .git so git
// works in-box) instead of the cwd — the boot form of `dabs recipe --worktree`.
func (r Real) upDetached(arg, worktree, nodeName string, startCommand bool) error {
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
	if err := checkSockets(name, rec.Sockets); err != nil {
		return err
	}
	// `--detach` starts the recipe's OWN command, so a recipe with no box to run it
	// in, or no command to run, has nothing to detach. Both refuse instead of
	// quietly booting: `--no-command` is the flag that means "boot, run nothing",
	// and a caller who asked for the command running must not be told it is when it
	// is not. A commandless recipe gets the same refusal a plain `dabs recipe` gives.
	if startCommand {
		if boxless {
			return fmt.Errorf("recipe %q: has no image, so there is no box to run a command in", name)
		}
		if len(rec.Command) == 0 {
			return fmt.Errorf("recipe %q: no command to run — `dabs recipe %s --no-command` boots the box without one", name, name)
		}
	}
	// A recipe with no image is a recipe for a PLACE, not a box. `--no-command` on
	// one provisions its nodes and stops — the same outcome as a plain
	// `dabs recipe`, so the two paths agree instead of the flag erroring on a
	// boxless recipe.
	if boxless {
		return r.provisionNodes(name, rec, worktree, nodeName)
	}
	box, logFile, kept, err := r.bootDetached(name, rec, worktree, nodeName, startCommand)
	if err != nil {
		return err
	}
	// Neither form tears the box down: the box outlives the call by construction,
	// and `keep` decides the fate of a box dabs is WAITING on — nothing is waiting
	// here, so there is no exit to reap at. The box is the user's, to reap with
	// `dabs rm`.
	for _, k := range kept {
		fmt.Fprintln(os.Stdout, tui.Success("kept: %s", k))
	}
	printUp(name, box.ID, box.Instance, rec, logFile)
	return nil
}

// bootDetached brings a NEW pristine box up from an already-resolved recipe
// VALUE and hands the caller back the box's identity, the host file a detached
// command's output goes to (empty when no command was started), and the places
// the boot kept. startCommand picks the form: false runs nothing, true starts
// the recipe's own command in the BACKGROUND inside the box. It tears nothing
// down on success and prints nothing — every user-facing message belongs to the
// caller.
func (r Real) bootDetached(name string, rec recipe.Recipe, worktree, nodeName string, startCommand bool) (Box, string, []string, error) {
	// Booting a box from inside a dabs worktree's own checkout parents the box on
	// that worktree, exactly as an explicit --worktree would (which wins).
	if worktree == "" {
		owner, oerr := r.resolveOwningWorktree()
		if oerr != nil {
			return Box{}, "", nil, oerr
		}
		worktree = owner
	}
	// `--worktree <wt>` binds an existing worktree to the `.` source (mounting its
	// parent .git so git works in-box) instead of cutting a fresh place.
	sources := rec.Sources
	if worktree != "" {
		full, werr := r.resolveWorktreeArg(worktree)
		if werr != nil {
			return Box{}, "", nil, werr
		}
		worktree = full
		bound, berr := r.bindWorktree(name, rec.Sources, worktree)
		if berr != nil {
			return Box{}, "", nil, berr
		}
		sources = bound
	}
	drv, err := r.driverFor(rec.Target)
	if err != nil {
		return Box{}, "", nil, err
	}
	if err := checkSocketsReachable(name, rec.Sockets, drv); err != nil {
		return Box{}, "", nil, err
	}
	// ASK the driver, before the boot, so one that cannot hold a background command
	// refuses while nothing has been provisioned — never a box that quietly holds
	// no running command. The question is a METHOD, not a bare type assertion: a
	// driver reached through a wrapper is asserted as the WRAPPER, which answers
	// for whatever it wraps. The reason is the driver's own, so this never claims
	// a cause it cannot know.
	detacher, canDetach := drv.(sandbox.Detacher)
	if startCommand {
		if !canDetach {
			return Box{}, "", nil, fmt.Errorf("recipe %q: the %s driver cannot hold a detached command — use `dabs recipe %s --no-command` and `dabs exec`", name, drv.Kind(), name)
		}
		if cerr := detacher.CheckDetach(); cerr != nil {
			return Box{}, "", nil, fmt.Errorf("recipe %q: %w — use `dabs recipe %s --no-command` and `dabs exec`", name, cerr, name)
		}
	}
	// The image is resolved first — WITHOUT building the recipe's own
	// Dockerfile: the boot uses an image a prior `dabs build` produced (it
	// may run where no builder exists) — and the claim runs after it and every
	// other name-independent refusal, so a boot refused for those reasons has
	// not touched the name's holder (provisionNodes claims for the boxless
	// path above).
	image, err := r.resolveBuiltImage(drv, name, rec.Image, rec.Target)
	if err != nil {
		return Box{}, "", nil, err
	}
	if nodeName != "" {
		if err := r.claimNodeName(nodeName); err != nil {
			return Box{}, "", nil, err
		}
	}
	// Cut the PLACE first: a box names its parent's spaces ($PARENT_VOLUME), and a
	// parent must exist to be named.
	_, tip, hosts, kept, cut, err := r.provisionPlaces(name, snapshotRecipe(rec), sources, worktree)
	if err != nil {
		return Box{}, "", nil, err
	}
	boxID, vars, err := r.mintBoxNode(name, tip, nodeName)
	if err != nil {
		return Box{}, "", nil, err
	}
	// A detached command has no terminal, so its output goes to a file — and the
	// file belongs on the HOST, in the box node's own tmp space, where it is
	// readable without entering the box and is reaped with the node that produced
	// it. Binding that space at the box's log dir is what puts it there; the
	// source machinery does the rest, so the mount is prepared and validated
	// exactly like a recipe's own.
	logFile := ""
	if startCommand {
		tmp, terr := r.resolveNodeSpace(boxID, SpaceTmp)
		if terr != nil {
			return Box{}, "", nil, terr
		}
		logFile = filepath.Join(tmp, sandbox.DetachedLogName)
		sources = append(append([]recipe.Source{}, sources...),
			recipe.Source{Mkmount: tmp, Path: sandbox.DetachedLogDir})
	}
	resolved, err := r.validateSources(name, sources, vars, hosts)
	if err != nil {
		return Box{}, "", nil, err
	}
	sockets, err := r.resolveSockets(name, boxID, rec.Sockets, vars)
	if err != nil {
		return Box{}, "", nil, err
	}
	instance, err := r.buildBox(drv, name, boxID, tip, rec, image, sources, resolved, sockets, cut, nil)
	if err != nil {
		return Box{}, "", nil, err
	}
	// A box that cannot be ENTERED is not up: a source mounted over `/`, a
	// `workdir:` missing from the image, or a read-only parent masking an rw child
	// all let Up report success while every later exec fails `bwrap: Can't chdir`.
	// Enter once with a no-op; if that fails the boot did not really succeed —
	// reap the box so no unusable instance lingers and surface the driver's message.
	if _, serr := drv.Exec(instance, []string{"true"}); serr != nil {
		proxy.Reap(r.boxProxy(instance)) // the box is abandoned; reap its engine too, not just the box
		_ = drv.Down(instance)
		return Box{}, "", nil, fmt.Errorf("boot failed: box is not usable: %w", serr)
	}
	// A bound worktree is mounted, not cut, so buildBox never journals it — record
	// its box→worktree link here so `worktrees ls` shows the box as live.
	if worktree != "" {
		if data, derr := r.resolveNodeData(worktree); derr == nil {
			r.logWorktreeUp(instance, worktree, data, name)
		}
	}
	// `--detach` hands the recipe's command to the box and lets go: the command
	// runs on the box's own init with its output in the node's log file, and
	// this call returns while it is still running. A failure here is a boot that
	// did not deliver what was asked, so the box is reaped rather than left as an
	// idle shell the caller believes is working.
	if startCommand {
		if derr := detacher.Detach(instance, rec.Command); derr != nil {
			proxy.Reap(r.boxProxy(instance))
			_ = drv.Down(instance)
			return Box{}, "", nil, fmt.Errorf("recipe %q: starting the detached command: %w", name, derr)
		}
	}
	return Box{ID: boxID, Instance: instance}, logFile, kept, nil
}

// printUp reports what the boot did and what to do next. The box has two names:
// its NODE ID — the canonical, stable handle rm/exec resolve first — and the
// driver's INSTANCE name, minted after the box comes up and named after the
// IMAGE. The handle shown is the node id; the instance is kept on its own line so
// the mapping is not lost. The instance alone never says which recipe booted the
// box, and whether the recipe's command is RUNNING is the one thing the two boot
// forms differ in (users assume it is) — both facts, plus the commands that
// follow (reap, shell in, tail the output or run what the recipe encodes), are
// spelled out here rather than left for the reader to reconstruct.
//
// logFile, when set, is the host file a DETACHED command's output is going to;
// empty means no command was started.
func printUp(name, nodeID, instance string, rec recipe.Recipe, logFile string) {
	started := logFile != ""
	head := fmt.Sprintf("recipe booted: %s", tui.Accent(name))
	if rec.Target != "" {
		head += fmt.Sprintf(" (on %s)", rec.Target)
	}
	fmt.Fprintln(os.Stdout, tui.Success("%s", head))
	fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("id:"), tui.Accent(nodeID))
	fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("instance:"), instance)
	if started {
		fmt.Fprintf(os.Stdout, "%s %s\n", tui.Muted("detached, running:"), shellJoin(rec.Command))
	} else {
		fmt.Fprintln(os.Stdout, tui.Muted("(no command was run — the recipe's command is not started by `--no-command`)"))
	}
	fmt.Fprintf(os.Stdout, "%s dabs rm %s\n", tui.Muted("reap:"), nodeID)
	// A detached command has no terminal to print to. Its output is a plain host
	// file in the node's own directory, so following it is a plain tail — no box
	// to enter, and nothing for the reader to go looking for.
	if started {
		fmt.Fprintf(os.Stdout, "%s tail -f %s\n", tui.Muted("output:"), logFile)
	}
	// The "sh in:" line runs `dabs exec <id> -- sh`. When the recipe's own
	// command IS exactly `sh`, the "run recipe command:" line below renders the
	// identical argv, so printing both would repeat one command under two labels —
	// drop the "sh in:" line and let the recipe-command line stand for both.
	if started || len(rec.Command) != 1 || rec.Command[0] != "sh" {
		fmt.Fprintf(os.Stdout, "%s dabs exec %s -- sh\n", tui.Muted("sh in:"), nodeID)
	}
	// The recipe's command is already running; offering it as something to run
	// would invite a second copy.
	if started {
		return
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
