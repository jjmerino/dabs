// Command forward is the in-box egress forwarder: a single-purpose static binary
// that bridges a loopback TCP port to a mounted host proxy socket and brackets
// the box's real command. dabs carries it as embedded bytes and drops a copy
// into a proxy box at forwarder.ForwardPath — the box never receives the dabs
// CLI. It is NOT a dabs subcommand: it has its own main and no dabs dependencies
// beyond the plumbing package.
//
// Usage: forward <socket> <port> [-- cmd…]
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

func main() {
	args := os.Args[1:]
	usage := func(msg string) {
		fmt.Fprintf(os.Stderr, "forward: %s\nusage: forward <socket> <port> [-- cmd…]\n", msg)
		os.Exit(2)
	}
	if len(args) < 2 {
		usage("socket and port are required")
	}
	port, err := strconv.Atoi(args[1])
	if err != nil {
		usage(fmt.Sprintf("port %q is not a number", args[1]))
	}
	var argv []string
	switch {
	case len(args) == 2:
	case args[2] == "--":
		argv = args[3:]
	default:
		usage(fmt.Sprintf("unexpected argument %q", args[2]))
	}
	code, err := forwarder.Run(args[0], port, argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forward: %v\n", err)
	}
	os.Exit(code)
}
