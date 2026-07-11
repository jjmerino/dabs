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
		names = r.worktreeNames(base, names) // keep only dabs's own worktrees; skip journal/junk
		if len(names) == 0 {
			fmt.Fprintln(os.Stdout, tui.Muted("no worktrees"))
			return nil
		}
		// Liveness is log-derived: a worktree has a live box if its instance has
		// an `up` in log.jsonl with no later `down` (see liveByWorktree).
		live := r.liveByWorktree()
		rows := make([][]string, 0, len(names))
		for _, n := range names {
			path := filepath.Join(base, n)
			branch, dirty, ahead, err := r.data.GitState(path)
			if err != nil {
				rows = append(rows, []string{tui.Accent(n), path, "", tui.Warn("unreadable: %v", err)})
				continue
			}
			hasWork := dirty || ahead > 0
			box := "no box"
			if inst, ok := live[n]; ok {
				box = fmt.Sprintf("box %s live", inst)
			}
			detail := tui.Muted("branch %s · uncommitted=%v ahead=%d · %s", branch, dirty, ahead, box)
			rows = append(rows, []string{tui.Accent(n), path, tui.WorkState(hasWork), detail})
		}
		fmt.Fprintln(os.Stdout, tui.Rows([]string{"NAME", "WORKTREE", "STATE", "DETAIL"}, rows))
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
		names = r.worktreeNames(base, names) // never reap the journal or junk as if a worktree
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

// worktreeNames keeps only dabs's OWN worktrees from a raw listing of
// ~/.dabs/worktrees. dabs owns this directory and knows its own vocabulary, so
// rather than blocklisting the names of junk that shares it (the box-lifecycle
// journal, a macOS .DS_Store, any stray file), it recognizes its entries
// positively by convention: a real worktree is a directory that git resolves as
// a worktree checkout. Anything that doesn't conform is silently dropped —
// never an error, it simply isn't a worktree.
func (r Real) worktreeNames(base string, names []string) []string {
	out := names[:0:0]
	for _, n := range names {
		if r.isWorktree(filepath.Join(base, n)) {
			out = append(out, n)
		}
	}
	return out
}

// isWorktree reports whether path is one of dabs's own git worktree checkouts.
// The check is git's own recognition (a valid worktree resolves a common git
// dir); a non-directory, a plain directory, or any junk resolves nothing and is
// rejected without erroring.
func (r Real) isWorktree(path string) bool {
	_, err := r.data.GitCommonDir(path)
	return err == nil
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
