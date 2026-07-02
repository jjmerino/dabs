package actions

import "fmt"

// DownParams are the inputs to the Down action.
type DownParams struct {
	Manifest string // path to manifest file or dir containing one
}

// Down stops and removes the sandbox.
func Down(p DownParams) error {
	fmt.Printf("actions.Down(%+v): [NOT BUILT YET!]\n", p)
	return nil
}
