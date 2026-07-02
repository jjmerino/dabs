package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
)

// Down removes the named instance.
func (r Real) Down(p params.Down) error {
	if err := r.driver.Down(p.Instance); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s down\n", p.Instance)
	return nil
}
