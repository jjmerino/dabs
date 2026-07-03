package actions

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/jjmerino/dabs/core/sandbox"
)

// Real satisfies params.Actions on top of a FLEET of injected drivers: the
// local one plus any configured remote targets. Manifests pick where their
// sandboxes live ("target"); instance names resolve across the whole fleet.
type Real struct {
	drivers map[string]sandbox.Driver // key "local" + config target names
	order   []string                  // stable iteration order for ls
	harness fs.FS                     // bundled harness integrations (for install)
}

// New returns actions backed by the given drivers (listed in order) and the
// harness-integration filesystem used by install/uninstall.
func New(drivers map[string]sandbox.Driver, order []string, harness fs.FS) Real {
	return Real{drivers: drivers, order: order, harness: harness}
}

// driverFor resolves a manifest's target ("" = local) to its driver.
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
