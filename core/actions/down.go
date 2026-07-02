package actions

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// Down removes the instances matching the name. A name matching several
// instances is NOT an error: it reports the matches and asks for --force,
// which removes them all.
func (r Real) Down(p params.Down) error {
	removed, err := r.driver.Down(p.Instance, p.Force)
	var ambiguous sandbox.AmbiguousError
	if errors.As(err, &ambiguous) {
		fmt.Fprintf(os.Stdout, "%s matches the following instances: %s. Use --force to bring down all.\n",
			p.Instance, strings.Join(ambiguous.Matches, ", "))
		return nil
	}
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		fmt.Fprintf(os.Stdout, "nothing matches %s\n", p.Instance)
		return nil
	}
	for _, name := range removed {
		fmt.Fprintf(os.Stdout, "%s down\n", name)
	}
	return nil
}
