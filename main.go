package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/jjmerino/dabs/cli"
	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/sandbox"
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

	// Interrupting a box-creating command (`recipe`/`do`) mid-flight would
	// otherwise leave a live box behind, since a signal skips deferred teardown.
	// Snapshot the instances now and, on SIGINT/SIGTERM, best-effort down any
	// that appeared since — the box being created/run. Best-effort: it ignores
	// errors and never blocks if nothing is up.
	if creates := createsBox(os.Args[1:]); creates {
		installInterruptCleanup(drivers)
	}

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
	// A per-command help request (`dabs <cmd> --help`) is not an error: print
	// that command's own usage to stdout and exit 0 — no top-level menu dump.
	if h, ok := err.(cli.HelpRequestedError); ok {
		fmt.Fprint(os.Stdout, h.Text)
		return
	}
	// A box command that merely exited non-zero is the box command's failure,
	// not dabs's: mirror its exit code and print no spurious `dabs: …` line.
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
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

// createsBox reports whether argv runs a command that brings up a fresh box —
// the only case where an interrupt can leak one. `recipe`/`do` create-and-run;
// `run`/`exec` reuse an existing box and `up` intentionally leaves one behind.
func createsBox(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "recipe", "do":
		return true
	}
	return false
}

// installInterruptCleanup snapshots the current instances and, on SIGINT/SIGTERM,
// tears down any that appeared afterward before exiting non-zero. It is
// best-effort cleanup on interrupt: it swallows driver errors and exits 130
// even if no box is up.
func installInterruptCleanup(drivers map[string]sandbox.Driver) {
	before := snapshotInstances(drivers)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		for name, drv := range drivers {
			infos, err := drv.Ls()
			if err != nil {
				continue
			}
			for _, in := range infos {
				if !before[name+"\x00"+in.Name] {
					_ = drv.Down(in.Name)
				}
			}
		}
		os.Exit(130)
	}()
}

// snapshotInstances records the instances each driver reports right now, keyed
// by driver+name, so a later scan can tell which boxes are new.
func snapshotInstances(drivers map[string]sandbox.Driver) map[string]bool {
	seen := map[string]bool{}
	for name, drv := range drivers {
		infos, err := drv.Ls()
		if err != nil {
			continue
		}
		for _, in := range infos {
			seen[name+"\x00"+in.Name] = true
		}
	}
	return seen
}
