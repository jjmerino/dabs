package actions

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jjmerino/dabs/core/tui"
)

// spaceHolds reports whether a space has anything worth asking about. A space
// holds something only when its tree contains at least one real file; a tree of
// only empty directories, or an absent space, holds nothing.
func (r Real) spaceHolds(dir string) (bool, error) {
	entries, err := r.data.ReadDir(dir)
	if err != nil {
		return false, nil // absent top-level space — nothing to reap
	}
	return r.treeHoldsFile(dir, entries)
}

// treeHoldsFile walks the already-listed entries of dir and reports whether any
// of them, at any depth, is a file. ReadDir returns an error on a non-directory
// (the OS errors with ENOTDIR), so a child that fails to list is a file — and,
// conservatively, any unexpected listing error is treated as held rather than
// silently dropping data.
func (r Real) treeHoldsFile(dir string, entries []string) (bool, error) {
	for _, name := range entries {
		child := filepath.Join(dir, name)
		sub, err := r.data.ReadDir(child)
		if err != nil {
			return true, nil // a file (or unreadable) — the space holds data
		}
		held, err := r.treeHoldsFile(child, sub)
		if err != nil {
			return false, err
		}
		if held {
			return true, nil
		}
	}
	return false, nil
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
		fmt.Fprintln(os.Stdout, tui.Success("%s stopped", tui.Accent(m.name)))
	}
	return nil
}
