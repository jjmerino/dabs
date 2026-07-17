//go:build darwin

package main

import (
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/apple"
)

// localKind is this platform's driver identity, answerable before the driver
// is built — the lazy wrapper serves Kind() from it.
const localKind = "apple"

// localDriver is this platform's sandbox driver. Foreign-platform drivers
// are never compiled into this binary.
func localDriver() (sandbox.Driver, error) {
	return apple.New()
}
