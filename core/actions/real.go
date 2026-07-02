package actions

import "github.com/jjmerino/dabs/core/sandbox"

// Real satisfies params.Actions on top of an injected sandbox.Driver.
// Callers (cli, RPC) receive it — or a fake — at construction time.
type Real struct {
	driver sandbox.Driver
}

// New returns actions backed by drv.
func New(drv sandbox.Driver) Real {
	return Real{driver: drv}
}
