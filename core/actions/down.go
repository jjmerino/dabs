package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
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
		fmt.Fprintf(os.Stdout, "nothing matches %s\n", p.Instance)
		return nil
	}
	if p.Dry {
		fmt.Fprintf(os.Stdout, "%s matches: %s\n", p.Instance, names(matches))
		return nil
	}
	if len(matches) > 1 && !p.Force {
		fmt.Fprintf(os.Stdout, "%s matches the following instances: %s. Use --force to bring down all.\n",
			p.Instance, names(matches))
		return nil
	}
	for _, m := range matches {
		if err := m.driver.Down(m.name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s down\n", m.name)
	}
	return nil
}
