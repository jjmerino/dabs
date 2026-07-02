package actions

import (
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/mcpserve"
	"github.com/jjmerino/dabs/core/params"
)

// Mcp serves the dabash MCP tool on stdio, curried to the matched instance
// wherever in the fleet it lives: every dabash call executes inside that
// instance and nowhere else. Blocks until stdin closes (the MCP client
// hanging up).
func (r Real) Mcp(p params.Mcp) error {
	m, err := r.resolveOne(p.Instance)
	if err != nil {
		return err
	}
	return mcpserve.Serve(os.Stdin, os.Stdout, func(command, cwd string) (string, error) {
		line := command
		if cwd != "" {
			line = "cd " + shellQuote(cwd) + " && (" + command + ")"
		}
		return m.driver.Exec(m.name, []string{"sh", "-c", line})
	})
}

// shellQuote single-quotes s for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
