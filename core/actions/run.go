package actions

import (
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Exec runs a command inside the matched instance — the single reach-in verb.
// With an exact argv (Shell false) it runs as-is, the lowest level with no
// shell interpretation. With Shell true the tokens are joined into one command
// line and wrapped in `sh -c`, so pipes, globs, redirects, and && work as
// written. The instance carries its own workdir and env (set at up time), so no
// manifest is involved. The command's own output is the output; dabs prints
// nothing around it.
func (r Real) Exec(p params.Exec) error {
	m, err := r.resolveOne(p.Instance)
	if err != nil {
		return err
	}
	cmd := p.Cmd
	if p.Shell {
		cmd = []string{"sh", "-c", strings.Join(p.Cmd, " ")}
	}
	return m.driver.Run(m.name, cmd)
}
