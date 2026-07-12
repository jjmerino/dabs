package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Down removes the instances matching the name, wherever in the fleet they
// live. All policy lives here, drivers only down exact names.
//
// Safety: a name is REQUIRED — an empty/blank name matches nothing (never
// "all"); matches() refuses one for every verb, so Down inherits it. A name
// matching more than one instance is REFUSED unless --multiple
// is set: it lists the matches and reaps nothing, so a stray prefix can't wipe
// several boxes at once. --force only skips the confirmation prompt; it does
// NOT by itself authorize multi-match reaping. --dry previews the matches.
func (r Real) Down(p params.Down) error {
	matches, err := r.matches(p.Instance)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("nothing matches %s", p.Instance))
		return nil
	}
	if p.Dry {
		fmt.Fprintf(os.Stdout, "%s %s %s\n", tui.Accent(p.Instance), tui.Muted("matches:"), names(matches))
		return nil
	}
	if len(matches) > 1 && !p.Multiple {
		fmt.Fprintln(os.Stdout, tui.Warn("%s matches %d instances: %s", p.Instance, len(matches), names(matches)))
		return fmt.Errorf("%q matches %d instances; pass --multiple to down all of them", p.Instance, len(matches))
	}
	for _, m := range matches {
		// Reap the box's spaces BEFORE the box, so a refusal leaves the box up and
		// the state intact rather than half-gone.
		if err := r.reapBoxSpaces(m.name, p.Force); err != nil {
			return err
		}
		if err := m.driver.Down(m.name); err != nil {
			return err
		}
		// If this instance is a live worktree-backed box in the journal, record
		// its `down` (best-effort — the log is the sole instance→worktree record).
		r.logWorktreeDown(m.name)
		fmt.Fprintln(os.Stdout, tui.Success("%s down", tui.Accent(m.name)))
	}
	return nil
}

// reapBoxSpaces applies the space rules to the box node behind an instance. The
// rules live in reapSpaces, so `down`, `rm` and `worktrees rm` cannot come to
// different conclusions about what a space means.
//
// A box holds no consent of its own: `down` reaps its tmp, asks about its
// ephemeral, and keeps its volume — printing where. What a box wants back next
// time belongs in its PLACE's spaces ($PARENT_VOLUME), which no box reaps.
//
// A box with no node has nothing to reap and is not an error.
func (r Real) reapBoxSpaces(instance string, force bool) error {
	n, ok := r.boxNodeFor(instance)
	if !ok {
		return nil
	}
	return r.reapSpaces(n, spacePolicy{yes: force})
}

// spaceHolds reports whether a space has anything worth asking about. Absent or
// empty holds nothing.
func (r Real) spaceHolds(dir string) (bool, error) {
	entries, err := r.data.ReadDir(dir)
	if err != nil {
		return false, nil // absent — nothing to reap
	}
	return len(entries) > 0, nil
}

// boxNodeFor finds the box node dabs wrote for an instance. A node id is minted
// before its box exists, so the link is recorded on the node, never derived from
// the name.
func (r Real) boxNodeFor(instance string) (Node, bool) {
	nodes, err := r.listNodes()
	if err != nil {
		return Node{}, false
	}
	for _, n := range nodes {
		if n.Kind == KindBox && n.Instance == instance {
			return n, true
		}
	}
	return Node{}, false
}

// downInstance brings one box down by exact name, wherever in the fleet it is.
// It is what `rm` uses on a box node: a place cannot be pulled out from under a
// running box.
func (r Real) downInstance(instance string) error {
	matches, err := r.matches(instance)
	if err != nil || len(matches) == 0 {
		return nil // already gone
	}
	for _, m := range matches {
		if err := m.driver.Down(m.name); err != nil {
			return err
		}
		r.logWorktreeDown(m.name)
		fmt.Fprintln(os.Stdout, tui.Success("%s down", tui.Accent(m.name)))
	}
	return nil
}
