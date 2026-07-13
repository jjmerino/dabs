package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// Prune reclaims the images a build left behind. A built image is not owned by
// any node — `rm` reaps nodes and their spaces, never the image the box booted
// from — so without this verb the image store only ever grows. It reaches every
// driver that keeps a local store (the sandbox.ImageStore capability); a driver
// without one (a remote server) is skipped.
//
// --dry lists what exists (names, sizes) and removes nothing. A live box still
// running on an image blocks that image's removal: without --force it is kept
// and the blocking boxes are named; with --force it is removed anyway.
func (r Real) Prune(p params.Prune) error {
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
		if p.Dry {
			for _, im := range imgs {
				fmt.Fprintln(os.Stdout, tui.Indent(fmt.Sprintf("%-28s %s", tui.Accent(im.Name), tui.Muted("%s", humanBytes(im.Size))), 2))
			}
			continue
		}
		r.pruneImages(store, r.drivers[key], imgs, p.Force)
	}
	if !any {
		fmt.Fprintln(os.Stdout, tui.Muted("no built images"))
		return nil
	}
	if p.Dry {
		fmt.Fprintln(os.Stdout, tui.Muted("\ndabs prune  — remove them (they rebuild on the next dabs build)"))
	}
	return nil
}

// pruneImages removes each image not blocked by a live box, reporting what was
// freed, what a still-running box holds back (unless force), and any driver
// refusal. An image is blocked when a live instance on the same driver was born
// from it — instance names are "<image>-<hex12>", so an image blocks every
// instance whose name is the image or is prefixed "<image>-".
func (r Real) pruneImages(store sandbox.ImageStore, drv sandbox.Driver, imgs []sandbox.Image, force bool) {
	live := map[string][]string{} // image name → live instance names born from it
	if !force {
		infos, err := drv.Ls()
		if err != nil {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Warn("cannot check for live boxes: %v", err), 2))
		}
		for _, in := range infos {
			img := imageOfInstance(in.Name)
			live[img] = append(live[img], in.Name)
		}
	}
	for _, im := range imgs {
		if boxes := live[im.Name]; len(boxes) > 0 {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Warn("%s kept: live box %s uses it (dabs rm it, or prune --force)", im.Name, strings.Join(boxes, ", ")), 2))
			continue
		}
		if err := store.RemoveImage(im.Name); err != nil {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Warn("%s kept: %v", im.Name, err), 2))
			continue
		}
		fmt.Fprintln(os.Stdout, tui.Indent(tui.Success("%s removed", tui.Accent(im.Name)), 2))
	}
}

// imageOfInstance recovers the image name from an instance name ("<image>-<hex12>").
func imageOfInstance(instance string) string {
	if i := strings.LastIndex(instance, "-"); i > 0 {
		return instance[:i]
	}
	return instance
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
