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

// parseUp parses `dabs up [--fresh] <manifest|dir>` arguments.
func parseUp(args []string) (params.Up, error) {
	var p params.Up
	fs := newFlagSet("up")
	fs.BoolVar(&p.Fresh, "fresh", false, "recreate the container == pristine state")
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

// parseDown parses `dabs down <name>` arguments (name as reported by ls).
func parseDown(args []string) (params.Down, error) {
	var p params.Down
	fs := newFlagSet("down")
	if err := fs.Parse(args); err != nil {
		return p, BadArgsError{Cmd: "down", Reason: err.Error()}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return p, BadArgsError{Cmd: "down", Reason: "expected exactly one <name> argument (see dabs ls)"}
	}
	p.Name = rest[0]
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
