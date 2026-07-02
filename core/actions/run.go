package actions

import "github.com/jjmerino/dabs/core/params"

// Run executes the command inside the matched instance. The instance
// carries its own workdir and env (set at up time), so no manifest is
// involved. The command's own output is the output; dabs prints nothing
// around it.
func (r Real) Run(p params.Run) error {
	instance, err := r.resolveOne(p.Instance)
	if err != nil {
		return err
	}
	return r.driver.Run(instance, p.Cmd)
}
