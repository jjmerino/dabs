package actions

import "github.com/jjmerino/dabs/core/params"

// Run executes the command inside the matched instance, wherever in the
// fleet it lives. The instance carries its own workdir and env (set at up
// time), so no manifest is involved. The command's own output is the
// output; dabs prints nothing around it.
func (r Real) Run(p params.Run) error {
	m, err := r.resolveOne(p.Instance)
	if err != nil {
		return err
	}
	return m.driver.Run(m.name, p.Cmd)
}
