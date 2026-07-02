package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
)

// Ls lists the sandboxes across the whole fleet: local plus every
// configured target, each row tagged with its driver and target.
func (r Real) Ls(params.Ls) error {
	total := 0
	for _, key := range r.order {
		infos, err := r.drivers[key].Ls()
		if err != nil {
			return err
		}
		for _, in := range infos {
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", in.Name, in.Status, in.Driver, key)
			total++
		}
	}
	if total == 0 {
		fmt.Fprintln(os.Stdout, "(no dabs sandboxes)")
	}
	return nil
}
