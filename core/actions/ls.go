package actions

import "fmt"

// LsParams are the inputs to the Ls action.
type LsParams struct{}

// Ls lists sandboxes.
func Ls(p LsParams) error {
	fmt.Printf("actions.Ls(%+v): [NOT BUILT YET!]\n", p)
	return nil
}
