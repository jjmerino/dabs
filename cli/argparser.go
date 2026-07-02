// Arg parsing: command-line arguments → core action params. Every parser is
// PURE: args in, a fully typed params object (or a BadArgsError) out. No I/O,
// no execution — commands.go composes these with the actions they feed.
//
// Parsers use the stdlib flag package (one FlagSet per command), so the
// standard Go convention applies: flags come BEFORE positional arguments
// (`dabs up --fresh <manifest>`).
package cli

import (
	"flag"
	"fmt"
	"io"

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

// parseBuild parses `dabs build <manifest|dir>` arguments.
func parseBuild(args []string) (params.Build, error) {
	var p params.Build
	fs := newFlagSet("build")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "build", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return p, BadArgsError{Cmd: "build", Reason: "expected exactly one <manifest|dir> argument"}
	}
	p.ManifestPath = rest[0]
	return p, nil
}

// parseUp parses `dabs up <manifest|dir>` arguments.
func parseUp(args []string) (params.Up, error) {
	var p params.Up
	fs := newFlagSet("up")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "up", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return p, BadArgsError{Cmd: "up", Reason: "expected exactly one <manifest|dir> argument"}
	}
	p.ManifestPath = rest[0]
	return p, nil
}

// parseRun parses `dabs run <instance> -- <cmd…>` arguments (instance as
// reported by ls, e.g. demo-0). The `--` is required: it makes explicit where
// dabs's arguments end and the sandboxed command begins.
func parseRun(args []string) (params.Run, error) {
	var p params.Run
	fs := newFlagSet("run")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "run", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) < 3 || rest[1] != "--" {
		return p, BadArgsError{Cmd: "run", Reason: "usage: run <instance> -- <cmd…> (see dabs ls)"}
	}
	p.Instance = rest[0]
	p.Cmd = rest[2:]
	return p, nil
}

// parseMcp parses `dabs mcp <instance>` arguments (instance as reported by
// ls).
func parseMcp(args []string) (params.Mcp, error) {
	var p params.Mcp
	fs := newFlagSet("mcp")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "mcp", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return p, BadArgsError{Cmd: "mcp", Reason: "expected exactly one <instance> argument (see dabs ls)"}
	}
	p.Instance = rest[0]
	return p, nil
}

// parseDown parses `dabs down [--force] <instance>` arguments (instance as
// reported by ls, e.g. demo-0; --force downs every instance the name
// matches).
func parseDown(args []string) (params.Down, error) {
	var p params.Down
	fs := newFlagSet("down")
	fs.BoolVar(&p.Force, "force", false, "down every instance the name matches")
	fs.BoolVar(&p.Dry, "dry", false, "only show what the name matches; down nothing")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "down", Reason: err.Error()}
	}
	// Accept flags AFTER the instance too (`dabs down exo --force`):
	// stdlib flag stops at the first positional, so pick them out of the rest.
	rest := fs.Args()[:0:0]
	for _, a := range fs.Args() {
		switch a {
		case "--force", "-force":
			p.Force = true
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
func parseLs(args []string) (params.Ls, error) {
	var p params.Ls
	fs := newFlagSet("ls")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "ls", Reason: err.Error()}
	}
	if len(fs.Args()) != 0 {
		return p, BadArgsError{Cmd: "ls", Reason: "takes no arguments"}
	}
	return p, nil
}
