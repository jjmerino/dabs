package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Worktrees inspects and reaps the git worktrees dabs recipes create under
// ~/.dabs/worktrees. Recipes KEEP worktrees so an agent's work is never lost;
// this is how you see what they did and clean up — WITHOUT silently discarding
// unreviewed work: a worktree with uncommitted changes or unmerged commits is
// refused unless --force (the explicit approval to lose it).
func (r Real) Worktrees(p params.Worktrees) error {
	home, err := r.data.HomeDir()
	if err != nil {
		return err
	}
	base := filepath.Join(home, ".dabs", "worktrees")

	switch p.Sub {
	case "", "ls":
		names, err := r.data.ReadDir(base)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Fprintln(os.Stdout, tui.Muted("no worktrees"))
			return nil
		}
		rows := make([][]string, 0, len(names))
		for _, n := range names {
			branch, dirty, ahead, err := r.data.GitState(filepath.Join(base, n))
			if err != nil {
				rows = append(rows, []string{tui.Accent(n), tui.Warn("unreadable: %v", err), "", ""})
				continue
			}
			hasWork := dirty || ahead > 0
			detail := tui.Muted("uncommitted=%v, ahead=%d", dirty, ahead)
			rows = append(rows, []string{tui.Accent(n), branch, tui.WorkState(hasWork), detail})
		}
		fmt.Fprintln(os.Stdout, tui.Rows([]string{"WORKTREE", "BRANCH", "STATE", "DETAIL"}, rows))
		return nil

	case "diff":
		if p.Name == "" {
			return fmt.Errorf("worktrees diff: a worktree name is required")
		}
		d, err := r.data.GitDiff(filepath.Join(base, p.Name))
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, d)
		return nil

	case "rm":
		if p.Name == "" {
			return fmt.Errorf("worktrees rm: a worktree name is required")
		}
		return r.reapWorktree(filepath.Join(base, p.Name), p.Force)

	case "prune":
		names, err := r.data.ReadDir(base)
		if err != nil {
			return err
		}
		var kept []string
		for _, n := range names {
			if err := r.reapWorktree(filepath.Join(base, n), p.Force); err != nil {
				kept = append(kept, n)
			}
		}
		if len(kept) > 0 {
			fmt.Fprintln(os.Stdout, tui.Warn("kept %d worktree(s) with unreviewed work: %s", len(kept), strings.Join(kept, ", ")))
			fmt.Fprintln(os.Stdout, tui.Muted("review with `dabs worktrees diff <name>`, then `prune --force` to discard"))
		}
		return nil

	default:
		return fmt.Errorf("worktrees: unknown subcommand %q (ls | diff <name> | rm <name> | prune)", p.Sub)
	}
}

// reapWorktree removes one worktree, refusing to discard unreviewed work unless
// force approves it.
func (r Real) reapWorktree(path string, force bool) error {
	_, dirty, ahead, err := r.data.GitState(path)
	if err != nil {
		return err
	}
	if (dirty || ahead > 0) && !force {
		name := filepath.Base(path)
		return fmt.Errorf("%s has unreviewed work (uncommitted=%v, %d commit(s) ahead) — review with `dabs worktrees diff %s`, then rm --force to discard", name, dirty, ahead, name)
	}
	if err := r.data.GitRemoveWorktree(path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, tui.Success("removed %s", tui.Accent(filepath.Base(path))))
	return nil
}
