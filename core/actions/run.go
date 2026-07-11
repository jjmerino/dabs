package actions

import (
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Exec runs an EXACT argv inside the matched instance — the lowest level, no
// shell interpretation. The instance carries its own workdir and env (set at up
// time), so no manifest is involved. The command's own output is the output;
// dabs prints nothing around it.
func (r Real) Exec(p params.Exec) error {
	m, err := r.resolveOne(p.Instance)
	if err != nil {
		return err
	}
	return m.driver.Run(m.name, p.Cmd)
}

// Run is the friendly level above Exec: it runs a shell command LINE inside the
// matched instance by wrapping it in `sh -c`, so pipes, globs, redirects, and
// && work as written. Tokens are joined with spaces into that one command line.
func (r Real) Run(p params.Run) error {
	m, err := r.resolveOne(p.Instance)
	if err != nil {
		return err
	}
	return m.driver.Run(m.name, []string{"sh", "-c", strings.Join(p.Cmd, " ")})
}
