package actions

// Real dispatches to the real action implementations. It exists so callers
// (cli, RPC) can depend on an interface they define and inject this — or a
// fake — at construction time.
type Real struct{}

func (Real) Up(p UpParams) error     { return Up(p) }
func (Real) Down(p DownParams) error { return Down(p) }
func (Real) Ls(p LsParams) error     { return Ls(p) }
