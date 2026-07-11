package actions

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// Real satisfies params.Actions on top of a FLEET of injected drivers: the
// local one plus any configured remote targets. Manifests pick where their
// sandboxes live ("target"); instance names resolve across the whole fleet.
type Real struct {
	drivers map[string]sandbox.Driver // key "local" + config target names
	order   []string                  // stable iteration order for ls
	harness fs.FS                     // bundled harness integrations (for install)
	images  fs.FS                     // bundled build recipes (for auth, …)
	data    data.Data                 // host effects (fs/env/git) — the testable seam
	confirm func(string) bool         // look-before-run gate; defaults to tui.Confirm
}

// New returns actions backed by the given drivers (listed in order), the
// harness-integration and image filesystems, and the host-effects layer.
func New(drivers map[string]sandbox.Driver, order []string, harness, images fs.FS, d data.Data) Real {
	return Real{drivers: drivers, order: order, harness: harness, images: images, data: d, confirm: tui.Confirm}
}

// WithConfirm returns a copy of r whose look-before-run gate is fn, so tests can
// answer the confirmation without a terminal.
func (r Real) WithConfirm(fn func(string) bool) Real {
	r.confirm = fn
	return r
}

// driverFor resolves a recipe's target ("" = local) to its driver.
func (r Real) driverFor(target string) (sandbox.Driver, error) {
	key := target
	if key == "" {
		key = "local"
	}
	drv, ok := r.drivers[key]
	if !ok {
		return nil, fmt.Errorf("no sandbox target %q (known: %s)", key, strings.Join(r.order, ", "))
	}
	return drv, nil
}
