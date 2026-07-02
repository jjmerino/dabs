package actions

import "fmt"

// UpParams are the inputs to the Up action.
type UpParams struct {
	Manifest string // path to manifest file or dir containing one
	Fresh    bool   // recreate the container == pristine state
}

// Up starts the sandbox.
func Up(p UpParams) error {
	fmt.Printf("actions.Up(%+v): [NOT BUILT YET!]\n", p)
	return nil
}
