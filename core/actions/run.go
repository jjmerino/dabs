package actions

import "github.com/jjmerino/dabs/core/params"

// Run executes the command inside the named instance. The instance carries
// its own workdir and env (set at up time), so no manifest is involved.
// The command's own output is the output; dabs prints nothing around it.
func (r Real) Run(p params.Run) error {
	return r.driver.Run(p.Instance, p.Cmd)
}
