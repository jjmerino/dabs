// Command forward is the in-box forwarder: a single-purpose static binary that
// bridges between a box's loopback ports and the unix sockets dabs mounts into
// it. dabs carries it as embedded bytes and drops a copy into a box at
// forwarder.ForwardPath — the box never receives the dabs CLI. It is NOT a dabs
// subcommand: it has its own main, and depends only on the plumbing packages.
//
// Egress: it bridges a loopback TCP port to the mounted host proxy socket and
// brackets the box's real command.
//
// Ingress: `forward publish` puts a box-local port behind a name on the box
// door, the one socket dabs mounts for everything that crosses the boundary.
//
// Usage: forward <socket> <port> [-- cmd…]
//
//	forward publish <name> --type webui|general --port <n>
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jjmerino/dabs/core/door"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

func main() {
	args := os.Args[1:]
	usage := func(msg string) {
		fmt.Fprintf(os.Stderr, "forward: %s\nusage: forward <socket> <port> [-- cmd…]\n       forward publish <name> --type webui|general --port <n>\n", msg)
		os.Exit(2)
	}
	if len(args) > 0 && args[0] == "publish" {
		publish(args[1:])
		return
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

// publish serves one named service until the process dies. --door overrides the
// box door, for a box that mounts it elsewhere and for tests.
func publish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	typ := fs.String("type", door.TypeGeneral, "service type: webui|general")
	port := fs.Int("port", 0, "box-local loopback port to serve")
	doorPath := fs.String("door", door.BoxPath, "the box door this service is published on")
	fail := func(msg string) {
		fmt.Fprintf(os.Stderr, "forward publish: %s\nusage: forward publish <name> --type webui|general --port <n>\n", msg)
		os.Exit(2)
	}
	// The name comes first and the flags follow, which is how anyone types it;
	// Go's flag package stops at the first non-flag, so take the name off the
	// front before parsing.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		fail(err.Error())
	}
	if name == "" || fs.NArg() != 0 {
		fail("expected exactly one service name")
	}
	if *port <= 0 || *port > 65535 {
		fail(fmt.Sprintf("--port %d is not a port", *port))
	}
	// Publishing runs until the process dies; anything it returns ended it. The
	// box was not granted a door, the door refused this service, or the door
	// stopped answering — each says which, in one line, on stderr.
	if err := door.NewPublisher(*doorPath, os.Stderr).Publish(name, *typ, *port); err != nil {
		fmt.Fprintf(os.Stderr, "forward publish: %v\n", err)
		os.Exit(1)
	}
}
