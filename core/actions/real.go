package actions

import (
	"fmt"
	"strings"

	"github.com/jjmerino/dabs/core/sandbox"
)

// Real satisfies params.Actions on top of a FLEET of injected drivers: the
// local one plus any configured remote targets. Manifests pick where their
// sandboxes live ("target"); instance names resolve across the whole fleet.
type Real struct {
	drivers map[string]sandbox.Driver // key "local" + config target names
	order   []string                  // stable iteration order for ls
}

// New returns actions backed by the given drivers, listed in order.
func New(drivers map[string]sandbox.Driver, order []string) Real {
	return Real{drivers: drivers, order: order}
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
