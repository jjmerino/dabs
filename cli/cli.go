package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/jjmerino/dabs/core/actions"
)

// Actions is the seam between the cli and core: everything the commands can
// invoke. Defined here — where it is consumed — per Go interface convention;
// actions.Real satisfies it, tests inject a fake.
type Actions interface {
	Up(actions.UpParams) error
	Down(actions.DownParams) error
	Ls(actions.LsParams) error
}

// CLI dispatches parsed commands to the injected Actions.
type CLI struct {
	actions Actions
}

// New returns a CLI that delegates to a.
func New(a Actions) *CLI {
	return &CLI{actions: a}
}

// Run dispatches argv (without the program name). It performs no I/O and
// never exits; everything it can detect wrong is returned as a typed error
// (see errors.go).
func (c *CLI) Run(args []string) error {
	if len(args) == 0 {
		return NoCommandError{}
	}
	cmd, ok := Commands[args[0]]
	if !ok {
		return UnknownCommandError{Name: args[0]}
	}
	return cmd.Run(c, args[1:])
}

// Usage writes the command list to w.
func Usage(w io.Writer) {
	fmt.Fprintln(w, "usage: dabs <command> [args]")
	names := make([]string, 0, len(Commands))
	for name := range Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-6s %s\n", name, Commands[name].Help)
	}
}
