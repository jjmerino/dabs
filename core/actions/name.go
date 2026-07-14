package actions

import (
	"fmt"
	"os"
	"regexp"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// A chosen node name becomes the node's ID — its one handle, shown wherever
// ids are shown and resolvable wherever ids resolve. The shape is the minted
// ids' own (letters, digits, dot, underscore, dash), so a name never needs
// quoting and never splits a row.
var nodeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// claimNodeName frees a requested node name before anything is minted. Names
// are unique across every known node; a holder whose subtree is INACTIVE — no
// live box, no real bytes, the empty markers `ls` hides — is a record, not
// data, so it is reaped on the spot rather than making every name single-use.
// An ACTIVE holder refuses the claim, and so does an unverifiable one: when a
// driver did not answer, silence is not proof the holder's box is gone.
func (r Real) claimNodeName(name string) error {
	if !nodeNameRe.MatchString(name) {
		return fmt.Errorf("--name %q: a name is letters, digits, dots, underscores, dashes — starting alphanumeric, at most 64", name)
	}
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	held := false
	for _, n := range nodes {
		if n.ID == name {
			held = true
			break
		}
	}
	if !held {
		return nil
	}
	ans := r.boxStates()
	if !ans.complete {
		return fmt.Errorf("--name %q: a node holds that name and a driver did not answer — cannot verify it is inactive", name)
	}
	active, _ := r.activeSubtrees(nodes, ans.state)
	if active[name] {
		return fmt.Errorf("--name %q: an active node holds that name (see dabs ls) — pick another, or reap it first", name)
	}
	fmt.Fprintln(os.Stdout, tui.Muted("name %s: held by an inactive node — reaping it", name))
	states := func() driversAnswer { return ans }
	if err := r.rmResolved(params.Rm{Node: name, Yes: true}, nodes, states); err != nil {
		return fmt.Errorf("--name %q: reap the inactive holder: %w", name, err)
	}
	return nil
}
