package main

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/cli"
	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/tui"
)

// main owns the process boundary: it installs the real actions, injects
// argv, prints errors generically, and translates cli errors into exit codes.
func main() {
	// All deps are constructed here, one per line, in dependency order.
	// Do not nest New calls — keep the wiring flat and readable.
	drivers, order, err := buildDrivers()
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.Failure("dabs: %v", err))
		os.Exit(1)
	}
	a := actions.New(drivers, order, imagesFS, data.OS{})
	c := cli.New(a)

	// Help is not an error: render it to stdout and exit 0. Basic help points
	// agents at the full guide; the full guide is the bundled AGENTS.md.
	if args := os.Args[1:]; len(args) == 1 {
		switch args[0] {
		case "-h", "--help", "help":
			cli.Usage(os.Stdout)
			return
		case "--help-full", "--help-full-for-agents":
			os.Stdout.Write(agentsGuide)
			return
		}
	}

	err = c.Run(os.Args[1:])
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "dabs: %v\n", err)
	switch err.(type) {
	case cli.NoCommandError, cli.UnknownCommandError, cli.BadArgsError:
		fmt.Fprintln(os.Stderr)
		cli.Usage(os.Stderr)
		os.Exit(2)
	}
	os.Exit(1)
}
