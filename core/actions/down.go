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

// reapBoxSpaces empties the box node behind an instance. ALL THREE spaces go: a
// box node is minted fresh every run and never re-entered, so nothing left in one
// can ever be reached again. What a box wants back next time belongs in its
// PLACE's spaces ($PARENT_VOLUME), which no box reaps.
//
// tmp/ and volume/ go silently. ephemeral/ goes only with consent when it holds
// something — that is the space a recipe names for bytes worth a question.
//
// The node record stays: it is the marker of what ran and from where.
//
// The space decides, not the recipe, so `down` never has to interpret intent.
//
// A worktree lives in its OWN node's ephemeral space, not the box's, so `down`
// never reaps a checkout — `dabs worktrees rm` does, and it still refuses
// unreviewed work.
//
// A box with no node has nothing to reap and is not an error.
func (r Real) reapBoxSpaces(instance string, force bool) error {
	n, ok := r.boxNodeFor(instance)
	if !ok {
		return nil
	}
	for _, space := range []string{SpaceTmp, SpaceVolume} {
		dir, err := r.resolveNodeSpace(n.ID, space)
		if err != nil {
			return err
		}
		if err := r.data.RemoveAll(dir); err != nil {
			return fmt.Errorf("down: %s: %w", dir, err)
		}
	}

	eph, err := r.resolveNodeSpace(n.ID, SpaceEphemeral)
	if err != nil {
		return err
	}
	held, err := r.spaceHolds(eph)
	if err != nil {
		return err
	}
	if !held {
		return r.data.RemoveAll(eph)
	}
	if !force && !r.confirm(fmt.Sprintf("%s holds files that `down` will delete.\n%s\nDelete them?",
		tui.Accent(n.ID), tui.Muted(eph))) {
		return fmt.Errorf("down: %s: kept (its ephemeral space holds files)", n.ID)
	}
	return r.data.RemoveAll(eph)
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
