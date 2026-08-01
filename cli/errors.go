package cli

import "fmt"

// Errors owned by the cli component. Callers (main, an RPC shim, tests)
// translate these into exit codes / responses however they see fit.

// NoCommandError reports that argv named no command at all.
type NoCommandError struct{}

func (NoCommandError) Error() string { return "no command given" }

// VersionRequestedError signals that the user asked which dabs this is
// (`dabs version` or `dabs --version`). It carries the rendered line; main
// prints it to stdout and exits 0. It is not a usage error.
type VersionRequestedError struct{ Text string }

func (VersionRequestedError) Error() string { return "version requested" }

// UnknownCommandError reports that argv named a command that doesn't exist.
type UnknownCommandError struct{ Name string }

func (e UnknownCommandError) Error() string { return fmt.Sprintf("unknown command %q", e.Name) }
