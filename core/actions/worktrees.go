package actions

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Worktrees INSPECTS the worktree NODES dabs recipes provision under
// ~/.dabs/nodes/<id>/ (the checkout lives in that node's held space). Recipes KEEP
// worktrees so an agent's work is never lost; this is how you see what they did.
// Reaping is `dabs rm <name>` for one (with the unreviewed-work guard) or
// `dabs rm --clean-worktrees` to sweep every worktree that holds no such work.
//
// Every entry comes from a node record dabs wrote, so dabs only ever lists what
// it actually provisioned — it never guesses from the filesystem.
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
			// The one judgment every verb shares (see worktreeJudgment), so
			// `dabs ls` and `dabs worktrees ls` say the same thing about one
			// worktree — and a squash-merged branch reads no-diff in both.
			state := r.worktreeJudgment(path, dirty, ahead)
			box := "no box"
			if inst, ok := live[n.ID]; ok {
				box = fmt.Sprintf("box %s live", inst)
			}
			detail := tui.Muted("branch %s · recipe %s · uncommitted=%v ahead=%d · %s",
				branch, n.Recipe, dirty, ahead, box)
			rows = append(rows, []string{tui.Accent(n.ID), path, styleState(state), detail})
		}
		fmt.Fprintln(os.Stdout, tui.Rows([]string{"NAME", "WORKTREE", "STATE", "DETAIL"}, rows))
		return nil

	case "diff":
		if p.Name == "" {
			return fmt.Errorf("worktrees diff: a worktree name is required (see dabs worktrees ls)")
		}
		// The name resolves git-style against the node records — the same
		// exact-then-prefix rule cd/exec/rm use — so a bad name is a clean
		// refusal here, never a raw git error over an internal path.
		nodes, err := r.listNodes()
		if err != nil {
			return err
		}
		hits := matchNodes(p.Name, nodes)
		if len(hits) == 0 {
			return fmt.Errorf("worktrees diff: no node %q (see dabs ls)", p.Name)
		}
		if len(hits) > 1 {
			var ids []string
			for _, h := range hits {
				ids = append(ids, h.ID)
			}
			return fmt.Errorf("worktrees diff: %q matches %d nodes (%s) — name one", p.Name, len(hits), strings.Join(ids, ", "))
		}
		n := hits[0]
		if n.Worktree == nil {
			return fmt.Errorf("worktrees diff: %q is not a worktree node (see dabs worktrees ls)", n.ID)
		}
		path, err := r.resolveNodeData(n.ID)
		if err != nil {
			return err
		}
		// `git diff` is blind to untracked files, so an agent's net-new files —
		// often its whole contribution — would be invisible to a reviewer deciding
		// merge-vs-discard. Mark untracked files intent-to-add so they surface as
		// additions in the diff. Best-effort: the diff still runs if this fails,
		// and it leaves the reap guards (GitState) untouched, which already count
		// untracked files as work.
		exec.Command("git", "-C", path, "add", "--intent-to-add", ".").Run()
		d, err := r.data.GitDiff(path)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, d)
		return nil

	default:
		return fmt.Errorf("worktrees: unknown subcommand %q (ls | diff <name>) — to reap use `dabs rm <name>` or `dabs rm --clean-worktrees`", p.Sub)
	}
}
