package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Worktrees inspects and reaps the worktree NODES dabs recipes provision under
// ~/.dabs/nodes/<id>/ (the checkout lives in that node's ephemeral space). Recipes KEEP
// worktrees so an agent's work is never lost; this is how you see what they did
// and clean up — WITHOUT silently discarding unreviewed work: a worktree with
// uncommitted changes or unmerged commits is refused unless --force (the
// explicit approval to lose it).
//
// Every entry comes from a node record dabs wrote, so dabs only ever lists and
// reaps what it actually provisioned — it never guesses from the filesystem.
func (r Real) Worktrees(p params.Worktrees) error {
	switch p.Sub {
	case "", "ls":
		nodes, err := r.listWorktreeNodes()
		if err != nil {
			return err
		}
		if len(nodes) == 0 {
			fmt.Fprintln(os.Stdout, tui.Muted("no worktrees"))
			return nil
		}
		// Liveness is log-derived: a worktree has a live box if its instance has
		// an `up` in the journal with no later `down` (see liveByWorktree).
		live := r.liveByWorktree()
		rows := make([][]string, 0, len(nodes))
		for _, n := range nodes {
			path, err := r.resolveNodeData(n.ID)
			if err != nil {
				return err
			}
			branch, dirty, ahead, err := r.data.GitState(path)
			if err != nil {
				rows = append(rows, []string{tui.Accent(n.ID), path, "", tui.Warn("unreadable: %v", err)})
				continue
			}
			hasWork := dirty || ahead > 0
			box := "no box"
			if inst, ok := live[n.ID]; ok {
				box = fmt.Sprintf("box %s live", inst)
			}
			detail := tui.Muted("branch %s · recipe %s · uncommitted=%v ahead=%d · %s",
				branch, n.Recipe, dirty, ahead, box)
			rows = append(rows, []string{tui.Accent(n.ID), path, tui.WorkState(hasWork), detail})
		}
		fmt.Fprintln(os.Stdout, tui.Rows([]string{"NAME", "WORKTREE", "STATE", "DETAIL"}, rows))
		return nil

	case "diff":
		if p.Name == "" {
			return fmt.Errorf("worktrees diff: a worktree name is required")
		}
		path, err := r.resolveNodeData(p.Name)
		if err != nil {
			return err
		}
		d, err := r.data.GitDiff(path)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, d)
		return nil

	case "rm":
		if p.Name == "" {
			return fmt.Errorf("worktrees rm: a worktree name is required")
		}
		return r.reapWorktree(p.Name, p.Force)

	case "prune":
		nodes, err := r.listWorktreeNodes()
		if err != nil {
			return err
		}
		var kept []string
		for _, n := range nodes {
			if err := r.reapWorktree(n.ID, p.Force); err != nil {
				kept = append(kept, n.ID)
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

// reapWorktree removes one worktree node: git drops the checkout and its
// branch, then the node directory (record and all) goes with it. It refuses to
// discard unreviewed work unless force approves it.
func (r Real) reapWorktree(id string, force bool) error {
	n, err := r.readNode(id)
	if err != nil {
		return fmt.Errorf("no worktree %q (see: dabs worktrees ls)", id)
	}
	if n.Worktree == nil {
		return fmt.Errorf("node %q is not a worktree", id)
	}
	path, err := r.resolveNodeData(id)
	if err != nil {
		return err
	}
	_, dirty, ahead, err := r.data.GitState(path)
	if err != nil {
		return err
	}
	if (dirty || ahead > 0) && !force {
		return fmt.Errorf("%s has unreviewed work (uncommitted=%v, %d commit(s) ahead) — review with `dabs worktrees diff %s`, then rm --force to discard", id, dirty, ahead, id)
	}
	if err := r.data.GitRemoveWorktree(path); err != nil {
		return err
	}
	dir, err := r.resolveNodeDir(id)
	if err != nil {
		return err
	}
	if err := r.data.RemoveAll(dir); err != nil {
		return fmt.Errorf("worktree %s: removing node dir: %w", id, err)
	}
	fmt.Fprintln(os.Stdout, tui.Success("removed %s", tui.Accent(id)))
	return nil
}
