package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Down removes the instances matching the name. All policy lives here, the
// driver only downs exact names: --dry shows what matches and downs
// nothing; several matches without --force is informational (list + hint);
// --force downs every match.
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
		fmt.Fprintf(os.Stdout, "%s matches: %s\n", p.Instance, strings.Join(matches, ", "))
		return nil
	}
	if len(matches) > 1 && !p.Force {
		fmt.Fprintf(os.Stdout, "%s matches the following instances: %s. Use --force to bring down all.\n",
			p.Instance, strings.Join(matches, ", "))
		return nil
	}
	for _, name := range matches {
		if err := r.driver.Down(name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s down\n", name)
	}
	return nil
}
