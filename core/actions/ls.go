package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
)

// Ls lists the sandboxes the driver manages.
func (r Real) Ls(params.Ls) error {
	infos, err := r.driver.Ls()
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Fprintln(os.Stdout, "(no dabs sandboxes)")
		return nil
	}
	for _, in := range infos {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", in.Name, in.Status, in.Driver)
	}
	return nil
}
