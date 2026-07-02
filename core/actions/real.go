package actions

import "github.com/jjmerino/dabs/core/params"

// Real satisfies params.Actions with the real action implementations.
// Callers (cli, RPC) inject this — or a fake — at construction time.
type Real struct{}

func (Real) Up(p params.Up) error     { return Up(p) }
func (Real) Down(p params.Down) error { return Down(p) }
func (Real) Ls(p params.Ls) error     { return Ls(p) }
