package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
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
	fmt.Fprintln(w, tui.Heading("usage:")+" dabs <command> [args]")
	names := make([]string, 0, len(Commands))
	for name := range Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		rows = append(rows, []string{tui.Accent(name), Commands[name].Help})
	}
	fmt.Fprintln(w, tui.Indent(tui.Rows(nil, rows), 2))
	fmt.Fprintln(w, "\n"+tui.Muted("agents: `dabs --help-full-for-agents` prints the full guide (recipes, manifests, examples)"))
}
