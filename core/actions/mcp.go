package actions

import (
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/mcpserve"
	"github.com/jjmerino/dabs/core/params"
)

// Mcp serves the dabash MCP tool on stdio, curried to the named instance:
// every dabash call executes inside that instance and nowhere else. Blocks
// until stdin closes (the MCP client hanging up).
func (r Real) Mcp(p params.Mcp) error {
	return mcpserve.Serve(os.Stdin, os.Stdout, func(command, cwd string) (string, error) {
		line := command
		if cwd != "" {
			line = "cd " + shellQuote(cwd) + " && (" + command + ")"
		}
		return r.driver.Exec(p.Instance, []string{"sh", "-c", line})
	})
}

// shellQuote single-quotes s for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
