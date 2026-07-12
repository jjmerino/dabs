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

// reapWorktree removes one worktree node. It is `dabs rm` on that node: the same
// verb, the same space rules, the same git-work guard, so a worktree cannot be
// reaped by rules a plain node would not be.
//
// The refusal to discard unreviewed work without force lives in Rm's guard, which
// every reap path shares. reapWorktree validates the node is a worktree (so a bad
// name for `worktrees rm` reads as one) and passes force through as the approval.
func (r Real) reapWorktree(id string, force bool) error {
	n, err := r.readNode(id)
	if err != nil {
		return fmt.Errorf("no worktree %q (see: dabs worktrees ls)", id)
	}
	if n.Worktree == nil {
		return fmt.Errorf("node %q is not a worktree", id)
	}
	return r.Rm(params.Rm{Node: id, Yes: true, Volume: force, Force: force})
}
