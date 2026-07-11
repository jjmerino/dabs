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

		fmt.Fprintln(os.Stdout, tui.Heading(header))
		if err != nil {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Failure("%v", err), 2))
			continue
		}
		if len(infos) == 0 {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Muted("(no instances)"), 2))
			continue
		}
		rows := make([][]string, 0, len(infos))
		for _, in := range infos {
			rows = append(rows, []string{in.Name, tui.Status(in.Status)})
		}
		fmt.Fprintln(os.Stdout, tui.Indent(tui.Rows(nil, rows), 2))
	}
	return nil
}
