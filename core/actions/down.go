package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Down removes the instances matching the name, wherever in the fleet they
// live. All policy lives here, drivers only down exact names.
//
// Safety: a name is REQUIRED — an empty/blank name matches nothing (never
// "all"). A name matching more than one instance is REFUSED unless --multiple
// is set: it lists the matches and reaps nothing, so a stray prefix can't wipe
// several boxes at once. --force only skips the confirmation prompt; it does
// NOT by itself authorize multi-match reaping. --dry previews the matches.
func (r Real) Down(p params.Down) error {
	if strings.TrimSpace(p.Instance) == "" {
		return fmt.Errorf("a name is required (see dabs ls)")
	}
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
