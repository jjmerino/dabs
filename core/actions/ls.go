package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Ls lists sandboxes grouped by fleet member — local first, then each
// server — with one section per member even when it holds no instances.
// A server section shows its driver kind (e.g. ssh) so it is clear the box
// lives on a remote; while a server is queried (an ssh round-trip) a
// spinner runs on stderr.
func (r Real) Ls(params.Ls) error {
	for _, key := range r.order {
		drv := r.drivers[key]

		header := fmt.Sprintf("%s (%s)", key, drv.Kind())
		if key == "local" {
			header = fmt.Sprintf("local (%s, this machine)", drv.Kind())
		}

		var stop func()
		if key != "local" {
			stop = tui.Spinner(key)
		}
		infos, err := lsTimeout(drv, remoteTimeout)
		if stop != nil {
			stop()
		}
		if err != nil {
			fmt.Fprintf(os.Stdout, "%s\n  error: %v\n", header, err)
			continue
		}

		fmt.Fprintln(os.Stdout, header)
		if len(infos) == 0 {
			fmt.Fprintln(os.Stdout, "  (no instances)")
			continue
		}
		for _, in := range infos {
			fmt.Fprintf(os.Stdout, "  %s\t%s\n", in.Name, in.Status)
		}
	}
	return nil
}
