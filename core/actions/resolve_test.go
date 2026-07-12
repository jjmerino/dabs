package actions_test

// Tests for instance-name resolution shared by every verb that takes a name.
// The footgun: an empty/blank name is a prefix of EVERY instance, so a naive
// prefix match "matches" the whole fleet. Contract (AGENTS.md): an empty/blank
// name matches NOTHING — never "all" — on every verb, not just down.

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/params"
)

// CONTRACT: a blank name on run/exec/down is refused, reaches no driver, and is
// never reported as "ambiguous" — blank matches nothing, not everything.
func TestBlankInstanceNameMatchesNothingOnEveryVerb(t *testing.T) {
	verbs := map[string]func(actions.Real, string) error{
		"run":  func(a actions.Real, n string) error { return a.Run(params.Run{Instance: n, Cmd: []string{"echo"}}) },
		"exec": func(a actions.Real, n string) error { return a.Exec(params.Exec{Instance: n, Cmd: []string{"echo"}}) },
		"down": func(a actions.Real, n string) error { return a.Down(params.Down{Instance: n}) },
	}
	for _, name := range []string{"", "   ", "\t"} {
		for label, call := range verbs {
			drv := twoBoxes()
			err := call(newReal("", baseData(), drv), name)
			if err == nil {
				t.Fatalf("%s with name %q: want an error, got nil", label, name)
			}
			if strings.Contains(err.Error(), "ambiguous") {
				t.Errorf("%s with name %q: blank must match NOTHING, got %v", label, name, err)
			}
			if len(drv.runs) != 0 || len(drv.downs) != 0 || len(drv.execs) != 0 {
				t.Errorf("%s with name %q: touched boxes (runs=%v downs=%v)", label, name, drv.runs, drv.downs)
			}
		}
	}
}
