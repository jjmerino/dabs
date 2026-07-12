// Arg parsing: command-line arguments → core action params. Every parser is
// PURE: args in, a fully typed params object (or a BadArgsError) out. No I/O,
// no execution — commands.go composes these with the actions they feed.
//
// Parsers use the stdlib flag package (one FlagSet per command), so the
// standard Go convention applies: flags come BEFORE positional arguments
// (`dabs up --flag <recipe>`).
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

// parseUp parses `dabs up [recipe|path]` arguments — an optional recipe name, a
// dabs.yaml path, or nothing (the registry default).
func parseUp(args []string) (params.Up, error) {
	var p params.Up
	fs := newFlagSet("up")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("up", fs)}
		}
		return p, BadArgsError{Cmd: "up", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return p, BadArgsError{Cmd: "up", Reason: "expected an optional recipe name or dabs.yaml path"}
	}
	if len(rest) == 1 {
		p.Name = rest[0]
	}
	return p, nil
}

// parseExec parses `dabs exec <instance> -- <cmd…>` arguments (instance as
// reported by ls, e.g. demo-0). The `--` is required: it makes explicit where
// dabs's arguments end and the exact argv run in the box begins.
func parseExec(args []string) (params.Exec, error) {
	var p params.Exec
	fs := newFlagSet("exec")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("exec", fs)}
		}
		return p, BadArgsError{Cmd: "exec", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) < 3 || rest[1] != "--" {
		return p, BadArgsError{Cmd: "exec", Reason: "usage: exec <instance> -- <cmd…> (see dabs ls)"}
	}
	p.Instance = rest[0]
	p.Cmd = rest[2:]
	return p, nil
}

// parseRun parses `dabs run <instance> <shell command…>` arguments — the
// friendly form that runs a shell command line in the box (see the run action,
// which wraps it in `sh -c`). No `--` is required; a leading one is tolerated so
// `run <instance> -- <cmd>` works too. Everything after the instance is the
// command, flags included (flag parsing stops at the instance positional).
func parseRun(args []string) (params.Run, error) {
	var p params.Run
	// A leading -h/--help is dabs's own help; anything later is the box command,
	// so a `--help` meant for the in-box tool is still forwarded.
	if wantsHelp(args) {
		return p, HelpRequestedError{helpText("run", newFlagSet("run"))}
	}
	// Everything after the instance is the shell command line, verbatim — dashes
	// and all. Flag parsing must not reach into it, or a command word like `-x`
	// would be eaten as a dabs flag and leave an empty `sh -c`.
	if len(args) >= 2 && args[1] == "--" {
		args = append(args[:1:1], args[2:]...) // drop an optional -- separator
	}
	if len(args) < 2 {
		return p, BadArgsError{Cmd: "run", Reason: "usage: run <instance> <shell command…> — args are joined into one `sh -c` line; use `exec` for exact argv (see dabs ls)"}
	}
	p.Instance = args[0]
	p.Cmd = args[1:]
	return p, nil
}

// parseDown parses `dabs down [--force] [--multiple] <instance>` arguments
// (instance as reported by ls, e.g. demo-0). A name matching more than one
// instance is refused unless --multiple is passed; --force only skips
// confirmation.
func parseDown(args []string) (params.Down, error) {
	var p params.Down
	fs := newFlagSet("down")
	fs.BoolVar(&p.Force, "force", false, "skip the confirmation prompt")
	fs.BoolVar(&p.Multiple, "multiple", false, "act on all instances the name matches (required when it matches more than one)")
	fs.BoolVar(&p.Dry, "dry", false, "only show what the name matches; down nothing")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("down", fs)}
		}
		return p, BadArgsError{Cmd: "down", Reason: err.Error()}
	}
	// Accept flags AFTER the instance too (`dabs down demo --force`):
	// stdlib flag stops at the first positional, so pick them out of the rest.
	rest := fs.Args()[:0:0]
	for _, a := range fs.Args() {
		switch a {
		case "--force", "-force":
			p.Force = true
		case "--multiple", "-multiple":
			p.Multiple = true
		case "--dry", "-dry":
			p.Dry = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) != 1 {
		return p, BadArgsError{Cmd: "down", Reason: "expected exactly one <instance> argument (see dabs ls)"}
	}
	p.Instance = rest[0]
	return p, nil
}

// parseLs parses `dabs ls` arguments (there are none).
func parseRm(args []string) (params.Rm, error) {
	var p params.Rm
	fs := newFlagSet("rm")
	fs.BoolVar(&p.Yes, "y", false, "reap the ephemeral space too (the one that may hold work)")
	fs.BoolVar(&p.Volume, "volume", false, "reap the volume too — what a place keeps on purpose")
	fs.BoolVar(&p.Force, "force", false, "approve discarding a worktree that holds unreviewed git work")
	fs.BoolVar(&p.Multiple, "multiple", false, "act on all nodes the name matches (required when it matches more than one)")
	// `dabs rm <node> -y` is how anyone would type it, and Go's flag package stops
	// at the first non-flag argument. Hoist the flags so either order works.
	if err := fs.Parse(hoistFlags(args)); err != nil {
		if err == flag.ErrHelp {
			return p, HelpRequestedError{helpText("rm", fs)}
		}
		return p, BadArgsError{Cmd: "rm", Reason: err.Error()}
	}
	if fs.NArg() != 1 {
		return p, BadArgsError{Cmd: "rm", Reason: "usage: rm <node> [-y] [--volume] [--force]"}
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
