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
	images  fs.FS                     // bundled build recipes (…)
	data    data.Data                 // host effects (fs/env/git) — the testable seam
	confirm func(string) bool         // look-before-run gate; defaults to tui.Confirm
	// forwarder is a caller-supplied egress forwarder binary; empty means dabs's
	// embedded copy (see WithForwarder).
	forwarder string
	// relay starts a box's door relay; the default spawns one as a detached
	// child of this dabs (see WithRelay).
	relay startRelay
}

// New returns actions backed by the given drivers (listed in order), the
// image filesystem, and the host-effects layer.
func New(drivers map[string]sandbox.Driver, order []string, images fs.FS, d data.Data) Real {
	return Real{drivers: drivers, order: order, images: images, data: d, confirm: tui.Confirm, relay: spawnRelay}
}

// WithConfirm returns a copy of r whose look-before-run gate is fn, so tests can
// answer the confirmation without a terminal.
func (r Real) WithConfirm(fn func(string) bool) Real {
	r.confirm = fn
	return r
}

// WithRelay returns a copy of r that starts a granted box's door relay with fn
// instead of spawning one, so a test can drive a boot without a process of its
// own. fn is handed the door socket, the registry directory and the log path,
// and answers with the pid to record on the box node.
func (r Real) WithRelay(fn func(doorPath, dir, logPath string) (int, error)) Real {
	r.relay = fn
	return r
}

// WithForwarder returns a copy of r that boots proxy-egress boxes with the
// forwarder binary at path instead of dabs's embedded copy, which a program
// embedding dabs as a module has no way to obtain — forward.bin is generated at
// build time and ships in neither the repo nor the module zip. An explicit path
// wins over any embed, so a caller that supplies one gets exactly that binary
// whatever the build tags say.
//
// The contract is the forwarder PROTOCOL — `<bin> <sockPath> <port> -- <argv…>`,
// binding loopback before exec'ing the argv, as egressforwarder/cmd/forward
// implements it — not a particular build of it. A superset speaking that
// protocol is fine: dabs mounts the binary into the box and never compares it to
// the embedded copy or pins its version. It must run on the box's platform, the
// same requirement the embed carries; dabs checks only that the path is an
// existing regular file, and reports a missing one at boot.
//
// The linux drivers mount this binary into the box, so it is the one that runs.
// The apple driver mounts no host binary — a host binary cannot run in the linux
// micro-VM, so on that driver the box image must carry a forwarder at
// /run/dabs/forward and the image's copy is what runs; the supplied path only
// satisfies provisioning there.
func (r Real) WithForwarder(path string) Real {
	r.forwarder = path
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
