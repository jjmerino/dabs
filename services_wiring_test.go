package main

import (
	"testing"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/sandbox"
	dockerdrv "github.com/jjmerino/dabs/core/sandbox/docker"
)

// realDrivers builds the drivers this machine can build, by kind — the same
// values driver.go wires, NOT wrapped in Lazy. The wrapper answers for every
// optional capability, so asking it whether a driver has one always says yes.
func realDrivers(t *testing.T) map[string]sandbox.Driver {
	t.Helper()
	out := map[string]sandbox.Driver{}
	if local, err := localDriver(); err == nil {
		out[local.Kind()] = local
	}
	if dkr, err := dockerdrv.New(); err == nil {
		out[dkr.Kind()] = dkr
	}
	if len(out) == 0 {
		t.Skip("no driver can be built here; NOT pinning the network-door list against the drivers")
	}
	return out
}

// CONTRACT: the kinds whose boxes are told to open a network door are exactly
// the kinds whose drivers can tell the host where the box is. A kind listed
// without the capability opens a listener nothing dials; a driver with the
// capability left off the list has every service on it read down for ever.
func TestNetworkDoorKindsMatchTheDriversThatAddressTheirBoxes(t *testing.T) {
	drivers := realDrivers(t)
	listed := map[string]bool{}
	for _, kind := range actions.NetworkDoorKinds() {
		listed[kind] = true
		drv, built := drivers[kind]
		if !built {
			t.Logf("driver %q cannot be built here; not checked", kind)
			continue
		}
		if _, can := drv.(sandbox.BoxAddresser); !can {
			t.Errorf("kind %q is told to open a network door, but its driver cannot say where the box is", kind)
		}
	}
	for kind, drv := range drivers {
		if _, can := drv.(sandbox.BoxAddresser); can && !listed[kind] {
			t.Errorf("driver %q can say where its box is, but %q boxes are never told to open the door — their services read down for ever", kind, kind)
		}
	}
}
