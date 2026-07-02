package actions

import (
	"fmt"

	"github.com/jjmerino/dabs/core/params"
)

// Down stops and removes the sandbox.
func Down(p params.Down) error {
	fmt.Printf("actions.Down(%+v): [NOT BUILT YET!]\n", p)
	return nil
}
