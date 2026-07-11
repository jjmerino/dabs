package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Down removes the instances matching the name, wherever in the fleet they
// live. All policy lives here, drivers only down exact names: --dry shows
// what matches and downs nothing; several matches without --force is
// informational (list + hint); --force downs every match.
func (r Real) Down(p params.Down) error {
	matches, err := r.matches(p.Instance)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("nothing matches %s", p.Instance))
		return nil
	}
	if p.Dry {
		fmt.Fprintf(os.Stdout, "%s %s %s\n", tui.Accent(p.Instance), tui.Muted("matches:"), names(matches))
		return nil
	}
	if len(matches) > 1 && !p.Force {
		fmt.Fprintln(os.Stdout, tui.Warn("%s matches %d instances: %s", p.Instance, len(matches), names(matches)))
		fmt.Fprintln(os.Stdout, tui.Muted("use --force to bring down all"))
		return nil
	}
	for _, m := range matches {
		if err := m.driver.Down(m.name); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, tui.Success("%s down", tui.Accent(m.name)))
	}
	return nil
}
