// Arg parsing: command-line arguments → core action params. Every parser is
// PURE: args in, a fully typed params object (or a BadArgsError) out. No I/O,
// no execution — commands.go composes these with the actions they feed.
//
// Parsers use the stdlib flag package (one FlagSet per command), so the
// standard Go convention applies: flags come BEFORE positional arguments
// (`dabs build --flag <recipe>`).
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// BadArgsError reports that a command's arguments don't parse.
type BadArgsError struct {
	Cmd    string
	Reason string
}

func (e BadArgsError) Error() string { return fmt.Sprintf("%s: %s", e.Cmd, e.Reason) }

// newFlagSet returns a FlagSet that reports errors by return value only,
// never by printing or exiting — keeping the parsers pure.
func newFlagSet(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseBuild parses `dabs build [recipe|path]` arguments — an optional recipe
// name, a dabs.yaml path, or nothing (the registry default).
func parseBuild(args []string) (params.Build, error) {
	var p params.Build
	fs := newFlagSet("build")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("build", fs)}
		}
		return p, BadArgsError{Cmd: "build", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return p, BadArgsError{Cmd: "build", Reason: "expected an optional recipe name or dabs.yaml path"}
	}
	if len(rest) == 1 {
		p.Name = rest[0]
	}
	return p, nil
}

// parseExec parses `dabs exec <instance> [--] <cmd…>` arguments (instance as
// reported by ls, e.g. demo-0). The `--` separator selects the mode: with it,
// what follows is an EXACT argv run as-is; without it, everything after the
// instance is a shell command line joined into one `sh -c` line (so
// pipes/globs/&& work). Flag parsing must not reach into the command, or a
// command word like `-x` would be eaten as a dabs flag.
func parseExec(args []string) (params.Exec, error) {
	var p params.Exec
	// A leading -h/--help is dabs's own help; anything later is the box command,
	// so a `--help` meant for the in-box tool is still forwarded.
	if wantsHelp(args) {
		return p, HelpRequestedError{helpText("exec", newFlagSet("exec"))}
	}
	// A leading `--` is the argv separator, not a node name: `exec -- echo hi`
	// names no box. Reject it as a usage error rather than letting `--` reach the
	// resolver as the node to match.
	if len(args) > 0 && args[0] == "--" {
		return p, BadArgsError{Cmd: "exec", Reason: "usage: exec <node> [--] <cmd…> — name the box before `--` (see dabs ls)"}
	}
	if len(args) < 2 {
		return p, BadArgsError{Cmd: "exec", Reason: "usage: exec <instance> [--] <cmd…> — `--` runs an exact argv, otherwise args join into one `sh -c` line (see dabs ls)"}
	}
	p.Instance = args[0]
	rest := args[1:]
	if rest[0] == "--" {
		if len(rest) < 2 {
			return p, BadArgsError{Cmd: "exec", Reason: "usage: exec <instance> -- <cmd…> (see dabs ls)"}
		}
		p.Cmd = rest[1:] // exact argv, run as-is
		return p, nil
	}
	p.Cmd = rest // shell command line
	p.Shell = true
	return p, nil
}

// parseRm parses `dabs rm <node> [flags]`. `rm` is the single reaper: it stops a
// box and removes its node and spaces. -y/--yes skips the consent prompt (stop a
// live box, reap a held space); --keep archives instead of removing; --dry
// previews; --volume additionally reaps the volume; --multiple authorizes a
// prefix that matches several nodes; --force is only for discarding unreviewed
// worktree git work — a different risk than the prompt -y skips.
func parseRm(args []string) (params.Rm, error) {
	var p params.Rm
	fs := newFlagSet("rm")
	fs.BoolVar(&p.Yes, "y", false, "skip the consent prompt: stop a live box and reap the held space")
	fs.BoolVar(&p.Yes, "yes", false, "skip the consent prompt (alias of -y)")
	fs.BoolVar(&p.Keep, "keep", false, "stop the box but ARCHIVE its node instead of removing it")
	fs.BoolVar(&p.Volume, "volume", false, "reap the volume too — what a place keeps on purpose")
	fs.BoolVar(&p.Multiple, "multiple", false, "act on all nodes the name matches (required when it matches more than one)")
	fs.BoolVar(&p.Dry, "dry", false, "preview what would be reaped; remove nothing")
	fs.BoolVar(&p.Force, "force", false, "approve discarding a worktree that holds unreviewed git work")
	fs.BoolVar(&p.CleanWorktrees, "clean-worktrees", false, "reap EVERY worktree with no unreviewed work (no node name); --force reaps them all")
	// `dabs rm <node> -y` is how anyone would type it, and Go's flag package stops
	// at the first non-flag argument. Hoist the flags so either order works.
	if err := fs.Parse(hoistFlags(args)); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("rm", fs)}
		}
		return p, BadArgsError{Cmd: "rm", Reason: err.Error()}
	}
	// --clean-worktrees sweeps the worktrees, so it takes no node name; every other
	// rm names exactly the one node (or prefix) to reap.
	if p.CleanWorktrees {
		if fs.NArg() != 0 {
			return p, BadArgsError{Cmd: "rm", Reason: "rm --clean-worktrees sweeps every clean worktree and takes no node name"}
		}
		return p, nil
	}
	if fs.NArg() != 1 {
		return p, BadArgsError{Cmd: "rm", Reason: "usage: rm <node> [-y] [--keep] [--volume] [--multiple] [--dry] [--force] | rm --clean-worktrees"}
	}
	p.Node = fs.Arg(0)
	return p, nil
}

// hoistFlags reorders argv so every -flag precedes the positional arguments,
// keeping their relative order. Go's flag package stops parsing at the first
// non-flag, so without this a trailing flag is silently read as a positional.
func hoistFlags(args []string) []string {
	var flags, rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}

func parseLs(args []string) (params.Ls, error) {
	var p params.Ls
	fs := newFlagSet("ls")
	fs.BoolVar(&p.All, "all", false, "also list archived nodes (boxes no driver holds any more)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("ls", fs)}
		}
		return p, BadArgsError{Cmd: "ls", Reason: err.Error()}
	}
	if len(fs.Args()) != 0 {
		return p, BadArgsError{Cmd: "ls", Reason: "takes no arguments"}
	}
	return p, nil
}
