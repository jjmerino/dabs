//go:build darwin

package main

import (
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/apple"
)

// localDriver is this platform's sandbox driver. Foreign-platform drivers
// are never compiled into this binary.
func localDriver() (sandbox.Driver, error) {
	return apple.New()
}
