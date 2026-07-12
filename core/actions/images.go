package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// Images lists the images a build left behind, and with Prune reclaims them.
// A built image is not owned by any node — `down`/`rm` reap nodes and their
// spaces, never the image the box booted from — so without this verb the image
// store only ever grows. It reaches every driver that keeps a local store (the
// sandbox.ImageStore capability); a driver without one (a remote server) is
// skipped.
func (r Real) Images(p params.Images) error {
	any := false
	for _, key := range r.order {
		store, ok := r.drivers[key].(sandbox.ImageStore)
		if !ok {
			continue
		}
		imgs, err := store.Images()
		if err != nil {
			fmt.Fprintln(os.Stdout, tui.Failure("%s: %v", key, err))
			continue
		}
		if len(imgs) == 0 {
			continue
		}
		any = true
		fmt.Fprintln(os.Stdout, tui.Heading(header(key, r.drivers[key].Kind())))
		if p.Prune {
			r.pruneImages(store, imgs)
			continue
		}
		for _, im := range imgs {
			fmt.Fprintln(os.Stdout, tui.Indent(fmt.Sprintf("%-28s %s", tui.Accent(im.Name), tui.Muted("%s", humanBytes(im.Size))), 2))
		}
	}
	if !any {
		fmt.Fprintln(os.Stdout, tui.Muted("no built images"))
		return nil
	}
	if !p.Prune {
		fmt.Fprintln(os.Stdout, tui.Muted("\ndabs images prune  — remove them (they rebuild on the next dabs build)"))
	}
	return nil
}

// pruneImages removes each image, reporting what was freed and what refused
// (an image a live box still uses is kept, not force-removed).
func (r Real) pruneImages(store sandbox.ImageStore, imgs []sandbox.Image) {
	for _, im := range imgs {
		if err := store.RemoveImage(im.Name); err != nil {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Warn("%s kept: %v", im.Name, err), 2))
			continue
		}
		fmt.Fprintln(os.Stdout, tui.Indent(tui.Success("%s removed", tui.Accent(im.Name)), 2))
	}
}

// humanBytes renders a byte count as a short human size; 0 (a driver that does
// not report size) renders as a dash.
func humanBytes(n int64) string {
	if n == 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
