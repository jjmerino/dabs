package main

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/cli"
	"github.com/jjmerino/dabs/core/actions"
)

// main owns the process boundary: it installs the real actions, injects
// argv, prints errors generically, and translates cli errors into exit codes.
func main() {
	// All deps are constructed here, one per line, in dependency order.
	// Do not nest New calls — keep the wiring flat and readable.
	drivers, order, err := buildDrivers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dabs: %v\n", err)
		os.Exit(1)
	}
	a := actions.New(drivers, order, harnessFS)
	c := cli.New(a)

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
