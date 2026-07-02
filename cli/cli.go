package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/jjmerino/dabs/core/params"
)

// CLI dispatches parsed commands to the injected params.Actions.
type CLI struct {
	actions params.Actions
}

// New returns a CLI that delegates to a.
func New(a params.Actions) *CLI {
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
