package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
)

// Down removes the named sandbox.
func (r Real) Down(p params.Down) error {
	if err := r.driver.Down(p.Name); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s down\n", p.Name)
	return nil
}
