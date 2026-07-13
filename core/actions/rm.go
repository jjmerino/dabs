package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Rm removes a node and what it holds. A node is a place dabs made, or a box, so
// one verb reaps either — and reaping a place reaps what stands on it.
//
// What happens to the bytes is decided by the SPACE they are in, never by the
// recipe or the kind. See reapSpaces.
//
// A box is brought down first: a place cannot be taken out from under a running
// box, and a box holding a mount is a box using the thing being removed.
func (r Real) Rm(p params.Rm) error {
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	targets, err := rmMatches(p.Node, nodes, p.Multiple)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		// A no-match reap is not an error, the same as `down`: naming a node that
		// isn't there leaves nothing to do, so a cleanup script can rely on the exit
		// status either way.
		fmt.Fprintln(os.Stdout, tui.Muted("no node %s", p.Node))
		return nil
	}

	// A node stood on is a node in use — everything above it goes with it. Gather
	// the whole set across every match, deduped and nearest-first, before anything
	// is touched, so a refusal loses nothing.
	var doomed []Node
	seen := map[string]bool{}
	for _, t := range targets {
		for _, n := range append([]Node{t}, descendantsOf(t, nodes)...) {
			if !seen[n.ID] {
				seen[n.ID] = true
				doomed = append(doomed, n)
			}
		}
	}
	// Build the view once: it is BOTH the preview and the source of the data
	// summary below, so the preview and the reap can never disagree about what
	// holds data. No live query is made, so a downed box reads as gone.
	views := r.viewNodes(doomed, nil)
	eph, vol, tmp := countHeldSpaces(views)

	// ONE question covers the whole set. Consent is needed to remove more than the
	// named node, or to delete data a reap would lose — a held ephemeral, or a held
	// volume with --volume. tmp is scratch and never needs asking. On yes the whole
	// set reaps with no further per-node prompts (batchYes carries that consent).
	batchYes := p.Yes
	showed := false
	if len(doomed) > 1 && !p.Yes {
		var b strings.Builder
		fmt.Fprintf(&b, "Removing %s reaps %d node(s):\n", tui.Accent(p.Node), len(doomed))
		b.WriteString(renderForest(views, rmColumns, 2))
		b.WriteString(reapDataSummary(eph, vol, tmp, p.Volume))
		// With no terminal there is nobody to ask, and asking anyway would block on
		// a pipe forever. Say what it would take, and take nothing.
		if !tui.Interactive() {
			fmt.Fprint(os.Stdout, b.String())
			return fmt.Errorf("rm %s: kept — pass -y to remove it and what stands on it", p.Node)
		}
		if !r.confirm(b.String() + "Proceed?") {
			return fmt.Errorf("rm %s: kept", p.Node)
		}
		batchYes = true
		showed = true
	}

	// A worktree node holds git work no space rule can see: uncommitted changes or
	// unpushed commits. Discarding that needs the explicit --force, not the -y that
	// only consents to the ephemeral space. The guard covers every worktree in the
	// cascade, so a childless leaf and a project reaping its descendants are held to
	// the same rule as `worktrees rm`. Checked before anything is touched, so a
	// refusal loses nothing.
	for _, n := range doomed {
		if err := r.guardWorktreeWork(n, p.Force); err != nil {
			return err
		}
	}

	// Deepest first: a box comes down before the place it was mounted on goes.
	for i := len(doomed) - 1; i >= 0; i-- {
		n := doomed[i]
		if n.Kind == KindBox && n.Instance != "" {
			if err := r.downInstance(n.Instance); err != nil {
				return fmt.Errorf("rm %s: %w", n.ID, err)
			}
		}
		if err := r.reapSpaces(n, spacePolicy{yes: batchYes, volume: p.Volume, removeNode: true, quiet: showed}); err != nil {
			return err
		}
	}
	return nil
}

// countHeldSpaces tallies, across a whole view forest, how many nodes hold data
// in each space — the aggregate a cascade reap asks about ONCE instead of node
// by node.
func countHeldSpaces(roots []*NodeView) (eph, vol, tmp int) {
	var walk func(v *NodeView)
	walk = func(v *NodeView) {
		if v.Ephemeral == CellHeld {
			eph++
		}
		if v.Volume == CellHeld {
			vol++
		}
		if v.Tmp == CellHeld {
			tmp++
		}
		for _, c := range v.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return
}

// reapDataSummary is the one-line-per-space footer under a cascade preview: what
// holds data, and what a reap does with it. ephemeral goes on proceed; volume is
// kept unless --volume; tmp is scratch and always cleared.
func reapDataSummary(eph, vol, tmp int, volume bool) string {
	var b strings.Builder
	if eph > 0 {
		b.WriteString(tui.Warn("%d node(s) hold ephemeral data — deleted on proceed", eph) + "\n")
	}
	if vol > 0 {
		if volume {
			b.WriteString(tui.Warn("%d node(s) hold volume data — deleted on proceed (--volume)", vol) + "\n")
		} else {
			b.WriteString(tui.Muted("%d node(s) hold volume data — kept (pass --volume to delete)", vol) + "\n")
		}
	}
	if tmp > 0 {
		b.WriteString(tui.Muted("%d node(s) hold tmp scratch — always cleared", tmp) + "\n")
	}
	return b.String()
}

// rmColumns are the columns a cascade preview draws: the tree, each node's
// kind, its three space cells, and its state. No WHERE column — a reap is about
// what is lost, not where it ran.
var rmColumns = []Column{ColNode, ColKind, ColVol, ColEph, ColTmp, ColState}

// guardWorktreeWork refuses to discard a worktree node that holds unreviewed git
// work — uncommitted changes or commits ahead — unless force approves it. Only a
// worktree node can answer this (git owns the state), so non-worktree nodes pass
// untouched. This is the same rule `worktrees rm` enforces, applied to every path
// that reaches a worktree, including a plain `dabs rm` and a project-wide cascade.
func (r Real) guardWorktreeWork(n Node, force bool) error {
	if n.Worktree == nil || force {
		return nil
	}
	path, err := r.resolveNodeData(n.ID)
	if err != nil {
		return err
	}
	_, dirty, ahead, err := r.data.GitState(path)
	if err != nil {
		return err
	}
	if dirty || ahead > 0 {
		return fmt.Errorf("%s has unreviewed work (uncommitted=%v, %d commit(s) ahead) — review with `dabs worktrees diff %s`, then rm --force to discard", n.ID, dirty, ahead, n.ID)
	}
	return nil
}

// spacePolicy is the consent a caller carries into a reap. `down` reaps a box's
// spaces with no consent beyond the down itself; `rm -y` consents to the
// ephemeral space; `rm -y --volume` also to the volume.
//
// removeNode is what separates the two verbs: `rm` takes the marker away, `down`
// leaves it. A downed box is ARCHIVED — what ran, and from where, outlives the
// box, and that is the whole reason a node exists.
// quiet suppresses the per-space "kept" line: a cascade reap already reported
// the aggregate (see reapDataSummary), so repeating it per node is noise.
type spacePolicy struct{ yes, volume, removeNode, quiet bool }

// reapSpaces applies the ONE rule about node spaces, so `down`, `rm` and
// `worktrees rm` cannot disagree about what a space means:
//
//	tmp/        always goes. It is scratch and it said so.
//	ephemeral/  goes with consent. Without it, it is KEPT and its path printed —
//	            this is where an agent's uncommitted afternoon lives, and a
//	            non-interactive caller must never lose it by default.
//	volume/     is KEPT unless asked for by name (--volume). It is what a place
//	            keeps ON PURPOSE; taking it silently would make the word a lie.
//
// The node record goes only when nothing of it is left. A node that still holds
// something is still a node.
func (r Real) reapSpaces(n Node, pol spacePolicy) error {
	tmp, err := r.resolveNodeSpace(n.ID, SpaceTmp)
	if err != nil {
		return err
	}
	if err := r.data.RemoveAll(tmp); err != nil {
		return fmt.Errorf("rm %s: %s: %w", n.ID, tmp, err)
	}

	kept := 0
	for _, sp := range []struct {
		space   string
		consent func(dir string) bool // asked ONLY when the space actually holds files
		how     string
	}{
		{SpaceEphemeral, func(dir string) bool { return pol.yes || r.consentToEphemeral(n, dir) }, "-y"},
		{SpaceVolume, func(string) bool { return pol.yes && pol.volume }, "-y --volume"},
	} {
		dir, err := r.resolveNodeSpace(n.ID, sp.space)
		if err != nil {
			return err
		}
		// spaceHolds is the ONE check for "does this space hold anything" — the
		// same one `ls`/the reap preview use — so the preview and the reap can
		// never disagree. Consent is sought only when it is actually held, so an
		// empty space is reaped silently and never prompts.
		held, err := r.spaceHolds(dir)
		if err != nil {
			return err
		}
		if held && !sp.consent(dir) {
			kept++
			if !pol.quiet {
				fmt.Fprintln(os.Stdout, tui.Warn("%s kept: %s", sp.space, dir)+
					tui.Muted("   (dabs rm %s %s to reap it)", n.ID, sp.how))
			}
			continue
		}
		// About to reap this space. A worktree's checkout lives in ephemeral, so
		// deregister it from git FIRST — while git can still resolve the repo from
		// the live checkout — or the reap strands a prunable registration + branch.
		if sp.space == SpaceEphemeral && pol.removeNode && n.Worktree != nil {
			if wt, e := r.resolveNodeData(n.ID); e == nil {
				_ = r.data.GitRemoveWorktree(wt)
			}
		}
		if err := r.data.RemoveAll(dir); err != nil {
			return fmt.Errorf("rm %s: %s: %w", n.ID, dir, err)
		}
	}

	if kept > 0 || !pol.removeNode {
		return nil // it still holds something, or the caller only emptied it
	}
	dir, err := r.resolveNodeDir(n.ID)
	if err != nil {
		return err
	}
	if err := r.data.RemoveAll(dir); err != nil {
		return fmt.Errorf("rm %s: %w", n.ID, err)
	}
	fmt.Fprintln(os.Stdout, tui.Success("%s removed", tui.Accent(n.ID)))
	return nil
}

// consentToEphemeral asks, when there is someone to ask. With no terminal there is
// no one, and silence is not consent: the space is kept, and the caller is told
// where it is. Asking anyway would block on a pipe forever — a reap that hangs
// waiting for an answer nobody can give is worse than one that keeps the files.
// consentToEphemeral asks before reaping a node's ephemeral space. It is called
// only when the space actually holds files (the loop checks spaceHolds first),
// so the prompt never fires for an empty space and never contradicts the reap
// preview. dir is the already-resolved space path.
func (r Real) consentToEphemeral(n Node, dir string) bool {
	if !tui.Interactive() {
		return false
	}
	return r.confirm(fmt.Sprintf("%s holds files in its ephemeral space.\n%s\nReap them?",
		tui.Accent(n.ID), tui.Muted("%s", dir)))
}

// rmMatches resolves the nodes a name reaps, git-style: a blank name matches
// nothing (never everything), an exact id is that one node even when it prefixes
// others, and a prefix matching several nodes is REFUSED unless multiple is set —
// mirroring how `down` guards a multi-match. A no-match returns no nodes and no
// error; the caller reports it and stops, so a reap of a name that isn't there is
// idempotent rather than a failure.
func rmMatches(name string, nodes []Node, multiple bool) ([]Node, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("a name is required (see dabs ls)")
	}
	var hits []Node
	for _, n := range nodes {
		if n.ID == name {
			return []Node{n}, nil
		}
		if strings.HasPrefix(n.ID, name) {
			hits = append(hits, n)
		}
	}
	if len(hits) > 1 && !multiple {
		var ids []string
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		fmt.Fprintln(os.Stdout, tui.Warn("%s matches %d nodes: %s", name, len(hits), strings.Join(ids, ", ")))
		return nil, fmt.Errorf("%q matches %d nodes; pass --multiple to rm all of them", name, len(hits))
	}
	return hits, nil
}

// descendantsOf returns every node standing on n, nearest first.
func descendantsOf(n Node, nodes []Node) []Node {
	var out []Node
	for _, c := range nodes {
		if c.Parent == n.ID {
			out = append(out, c)
			out = append(out, descendantsOf(c, nodes)...)
		}
	}
	return out
}
