package actions

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/door"
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
	// unrelay stops a box's door relay by the pid its node records; the default
	// kills its process group (see WithRelayReaper).
	unrelay func(pid int)
}

// New returns actions backed by the given drivers (listed in order), the
// image filesystem, and the host-effects layer.
func New(drivers map[string]sandbox.Driver, order []string, images fs.FS, d data.Data) Real {
	return Real{drivers: drivers, order: order, images: images, data: d, confirm: tui.Confirm, relay: spawnRelay, unrelay: reapRelay}
}

// WithConfirm returns a copy of r whose look-before-run gate is fn, so tests can
// answer the confirmation without a terminal.
func (r Real) WithConfirm(fn func(string) bool) Real {
	r.confirm = fn
	return r
}

// WithRelay returns a copy of r that starts a box's door relay with fn
// instead of spawning one, so a test can drive a boot without a process of its
// own. fn is handed the door socket, the registry directory ("" for a box that
// may not publish), the log path, the egress engine's socket ("" for a box
// with no proxy egress) and the carried sockets, and answers with the pid to
// record on the box node.
func (r Real) WithRelay(fn func(doorPath, dir, logPath, egress string, carries []door.Carry) (int, error)) Real {
	r.relay = fn
	return r
}

// WithRelayExecutable returns a copy of r whose box relays are spawned from
// the named dabs binary instead of this process's own executable. It is the
// LIBRARY consumer's knob: a box with any crossing (a publish grant, a proxy
// egress, a declared socket) is answered by a `services relay` process, and a
// program embedding dabs as a module is not a binary that serves that verb.
func (r Real) WithRelayExecutable(path string) Real {
	r.relay = func(doorPath, dir, logPath, egress string, carries []door.Carry) (int, error) {
		return spawnRelayFrom(path, doorPath, dir, logPath, egress, carries)
	}
	return r
}

// WithRelayReaper returns a copy of r that stops a box's door relay with fn
// instead of signalling its process group, so a test can watch a teardown reach
// the relay without a process to kill. fn is handed the pid the box node
// records.
func (r Real) WithRelayReaper(fn func(pid int)) Real {
	r.unrelay = fn
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
