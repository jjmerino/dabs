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
	if p.CleanWorktrees {
		return r.rmCleanWorktrees(p)
	}
	if p.Inactive {
		return r.rmInactive(p)
	}
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	return r.rmResolved(p, nodes, r.memoBoxStates())
}

// rmResolved is Rm past the lookups: the node list and the fleet query are the
// caller's, so a batch reap (rmInactive, rmCleanWorktrees) lists nodes once and
// asks the fleet once for the whole sweep instead of per node. states is called
// only when a box is among the doomed, so a reap that touches no box never
// pays a fleet query.
func (r Real) rmResolved(p params.Rm, nodes []Node, states func() map[string]boxState) error {
	targets, err := rmMatches(p.Node, nodes, p.Multiple)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		// A no-match reap is not an error: naming a node that isn't there leaves
		// nothing to do, so a cleanup script gets a stable exit status whether or
		// not the node still exists.
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
	// holds data. Space state comes from the view; box liveness comes from the
	// same fleet query `ls` runs, so the preview and `ls` say the same thing
	// about which boxes are running — a stop is a loss the preview must show.
	state := map[string]boxState{}
	for _, n := range doomed {
		if n.Kind == KindBox {
			state = states()
			break
		}
	}
	views := r.viewNodes(doomed, state)
	held, vol, tmp := countHoldingSpaces(views)

	// --keep keeps the record instead of removing: stop the box(es) but leave the
	// node record behind — teardown without forgetting what ran and from where. A
	// kept box with empty spaces becomes inactive and drops out of the default `ls`.
	if p.Keep {
		return r.archive(targets, p)
	}

	// --dry previews the reap and touches nothing.
	if p.Dry {
		fmt.Fprint(os.Stdout, rmPreview(p.Node, views, held, vol, tmp, p.Volume))
		return nil
	}

	live := false
	for _, n := range doomed {
		if _, up := state[n.Instance]; n.Kind == KindBox && up {
			live = true
			break
		}
	}

	// ONE question covers the whole set. Consent is needed to remove more than the
	// named node, to stop a live box, or to delete data a reap would lose — a held
	// space (something outside points at it), or a volume with --volume. tmp is
	// scratch and never needs asking. On yes the whole set reaps with no further
	// per-node prompts (batchYes carries that consent).
	batchYes := p.Yes
	showed := false
	if (len(doomed) > 1 || live || held > 0 || vol > 0) && !p.Yes {
		b := rmPreview(p.Node, views, held, vol, tmp, p.Volume)
		// With no terminal there is nobody to ask, and asking anyway would block on
		// a pipe forever. Say what it would take, and take nothing — exit nonzero so
		// a script sees the reap did not happen.
		if !tui.Interactive() {
			fmt.Fprint(os.Stdout, b)
			return fmt.Errorf("rm %s: kept — pass -y to remove it and what stands on it", p.Node)
		}
		if !r.confirm(b + "Proceed?") {
			return fmt.Errorf("rm %s: kept", p.Node)
		}
		batchYes = true
		showed = true
	}

	// A worktree node holds git work no space rule can see: uncommitted changes or
	// unpushed commits. Discarding that needs the explicit --force, not the -y that
	// only consents to the held space. The guard covers every worktree in the
	// cascade, so a childless leaf and a project reaping its descendants are held to
	// the same rule. Checked before anything is touched, so a refusal loses nothing.
	for _, n := range doomed {
		if err := r.guardWorktreeWork(n, p.Force); err != nil {
			return err
		}
	}

	// Deepest first: a box comes down before the place it was mounted on goes.
	// An instance the fleet did not report is already gone — no down to attempt,
	// no driver round-trip to pay for it.
	for i := len(doomed) - 1; i >= 0; i-- {
		n := doomed[i]
		if _, present := state[n.Instance]; n.Kind == KindBox && n.Instance != "" && present {
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

// rmCleanWorktrees sweeps EVERY worktree node in one shot, reaping the ones that
// hold no unreviewed git work and keeping the rest. It is the batch `dabs rm`
// over worktrees: each node reaps through the ordinary Rm path, so the same
// space rules and the same unreviewed-work guard apply. A worktree that holds
// work is refused (its reap returns an error) and reported at the end, unless
// --force approves discarding it. --dry previews each without removing anything.
func (r Real) rmCleanWorktrees(p params.Rm) error {
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	states := r.memoBoxStates()
	var kept []string
	for _, n := range nodes {
		if n.Worktree == nil {
			continue
		}
		one := params.Rm{Node: n.ID, Yes: true, Dry: p.Dry, Volume: false, Force: p.Force}
		if err := r.rmResolved(one, nodes, states); err != nil {
			if strings.Contains(err.Error(), "unreviewed work") {
				kept = append(kept, n.ID)
				continue
			}
			return fmt.Errorf("rm %s: %w", n.ID, err)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintln(os.Stdout, tui.Warn("kept %d worktree(s) with unreviewed work: %s", len(kept), strings.Join(kept, ", ")))
		fmt.Fprintln(os.Stdout, tui.Muted("review with `dabs worktrees diff <name>`, then `dabs rm <name> --force` to discard"))
	}
	return nil
}

// countHoldingSpaces tallies, across a whole view forest, how many nodes hold data
// in each space — the aggregate a cascade reap asks about ONCE instead of node
// by node.
func countHoldingSpaces(roots []*NodeView) (held, vol, tmp int) {
	var walk func(v *NodeView)
	walk = func(v *NodeView) {
		if v.Held == CellHolds {
			held++
		}
		if v.Volume == CellHolds {
			vol++
		}
		if v.Tmp == CellHolds {
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
// holds data, and what a reap does with it. a held space goes on proceed; volume
// is kept unless --volume; tmp is scratch and always cleared.
func reapDataSummary(held, vol, tmp int, volume bool) string {
	var b strings.Builder
	if held > 0 {
		b.WriteString(tui.Warn("%d node(s) hold a held space — deleted on proceed", held) + "\n")
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

// rmPreview renders what a reap would take: the forest of doomed nodes and the
// per-space data summary under it. It is shown by --dry, and as the body of the
// consent prompt, so the preview and the question can never disagree.
func rmPreview(name string, views []*NodeView, held, vol, tmp int, volume bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Removing %s reaps %d node(s):\n", tui.Accent(name), countViews(views))
	b.WriteString(renderForest(views, rmColumns, 2))
	b.WriteString(reapDataSummary(held, vol, tmp, volume))
	return b.String()
}

// countViews counts nodes across a view forest — the number a preview reports.
func countViews(views []*NodeView) int {
	n := 0
	var walk func(v *NodeView)
	walk = func(v *NodeView) {
		n++
		for _, c := range v.Children {
			walk(c)
		}
	}
	for _, v := range views {
		walk(v)
	}
	return n
}

// archive brings the matched box(es) down — the teardown `dabs rm --keep`
// performs. Only a box can be kept this way: a place has nothing to stop, and
// keeping it would be a no-op. The box is stopped, tmp is reaped, a held space is
// kept unless -y consents, and the volume is kept unless --volume. Bringing a box
// down takes its NODE too when nothing is left: an empty record is cruft, not
// history, so it does not linger as a `gone` row. A box that left files behind in
// a kept space keeps its node — there is something to point at.
func (r Real) archive(targets []Node, p params.Rm) error {
	for _, n := range targets {
		if n.Kind != KindBox {
			return fmt.Errorf("rm --keep %s: only a box can be kept (it is a %s)", n.ID, n.Kind)
		}
	}
	for _, n := range targets {
		if n.Instance != "" {
			if err := r.downInstance(n.Instance); err != nil {
				return fmt.Errorf("rm --keep %s: %w", n.ID, err)
			}
		}
		// removeNode: reapSpaces takes the node dir only when no space held anything
		// (nothing kept). A kept held space or a leftover volume keeps the record.
		if err := r.reapSpaces(n, spacePolicy{yes: p.Yes, volume: p.Volume, removeNode: true}); err != nil {
			return err
		}
	}
	return nil
}

// rmInactive sweeps EVERY inactive subtree — any node kind whose subtree holds no
// running box and no real files — reaping each through the ordinary Rm path so
// the same space rules apply. Nothing an inactive subtree holds is data (that is
// what makes it inactive), so the reap needs no consent; --dry previews each
// without removing anything. It is the reaper for the empty markers `ls` hides.
func (r Real) rmInactive(p params.Rm) error {
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	state := r.boxStates()
	_, inactiveRoots := r.activeSubtrees(nodes, state)
	if len(inactiveRoots) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("no inactive subtrees"))
		return nil
	}
	// Inactive roots are disjoint subtrees, so one node list and one fleet answer
	// serve every reap in the sweep.
	states := func() map[string]boxState { return state }
	for _, rid := range inactiveRoots {
		if err := r.rmResolved(params.Rm{Node: rid, Yes: true, Dry: p.Dry}, nodes, states); err != nil {
			return fmt.Errorf("rm --inactive %s: %w", rid, err)
		}
	}
	return nil
}

// memoBoxStates defers the fleet query and caches its answer: the fleet is
// asked the first time the answer is needed, and never again for this reap.
func (r Real) memoBoxStates() func() map[string]boxState {
	var state map[string]boxState
	return func() map[string]boxState {
		if state == nil {
			state = r.boxStates()
		}
		return state
	}
}

// rmColumns are the columns a cascade preview draws: the tree, each node's
// kind, its three space cells, and its state. No WHERE column — a reap is about
// what is lost, not where it ran.
var rmColumns = []Column{ColNode, ColKind, ColVol, ColHeld, ColTmp, ColState}

// guardWorktreeWork refuses to discard a worktree node that holds unreviewed git
// work — uncommitted changes or commits ahead — unless force approves it. Only a
// worktree node can answer this (git owns the state), so non-worktree nodes pass
// untouched. The one rule applies to every path that reaches a worktree: a named
// `dabs rm <wt>`, a project-wide cascade, and the `--clean-worktrees` sweep.
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

// spacePolicy is the consent a caller carries into a reap. `rm -y` consents to
// the held space; `rm -y --volume` also to the volume.
//
// removeNode is what separates a reap from a keep: with it, `rm` takes the
// marker away; without it, `rm --keep` leaves the record. A kept box holds what
// ran, and from where, after the box is gone — the whole reason a node exists.
// quiet suppresses the per-space "kept" line: a cascade reap already reported
// the aggregate (see reapDataSummary), so repeating it per node is noise.
//
// implicit marks a teardown that is not an `rm`: it never seeks interactive
// consent for a held space. A finished box being reaped keeps a held space that
// holds files (and its node with it) without stopping to ask, because nobody
// asked for the reap in the first place.
type spacePolicy struct{ yes, volume, removeNode, quiet, implicit bool }

// reapSpaces applies the ONE rule about node spaces, so `rm`, `rm --keep` and
// the `--clean-worktrees` sweep cannot disagree about what a space means:
//
//	tmp/        always goes. It is scratch and it said so.
//	held/       goes with consent. Without it, it is KEPT and its path printed —
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

	// The held space is resolved through resolveHeldSpace so a legacy node whose
	// space dir is named ephemeral/ is still guarded and reaped; its label stays
	// "held" (the current vocabulary) whatever the dir on disk is called.
	heldDir, err := r.resolveHeldSpace(n.ID)
	if err != nil {
		return err
	}
	volDir, err := r.resolveNodeSpace(n.ID, SpaceVolume)
	if err != nil {
		return err
	}

	kept := 0
	for _, sp := range []struct {
		space   string
		dir     string
		consent func(dir string) bool // asked ONLY when the space actually holds files
		how     string
	}{
		{SpaceHeld, heldDir, func(dir string) bool { return pol.yes || (!pol.implicit && r.consentToHeld(n, dir)) }, "-y"},
		{SpaceVolume, volDir, func(string) bool { return pol.yes && pol.volume }, "-y --volume"},
	} {
		dir := sp.dir
		// spaceHolds is the ONE check for "does this space hold anything" — the
		// same one `ls`/the reap preview use — so the preview and the reap can
		// never disagree. Consent is sought only when it is actually held, so an
		// empty space is reaped silently and never prompts.
		holds, err := r.spaceHolds(dir)
		if err != nil {
			return err
		}
		if holds && !sp.consent(dir) {
			kept++
			if !pol.quiet {
				fmt.Fprintln(os.Stdout, tui.Warn("%s kept: %s", sp.space, dir)+
					tui.Muted("   (dabs rm %s %s to reap it)", n.ID, sp.how))
			}
			continue
		}
		// About to reap this space. A worktree's checkout lives in the held space,
		// so deregister it from git FIRST — while git can still resolve the repo from
		// the live checkout — or the reap strands a prunable registration + branch.
		if sp.space == SpaceHeld && pol.removeNode && n.Worktree != nil {
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

// consentToHeld asks before reaping a node's held space. It is called only when
// the space actually holds files (the loop checks spaceHolds first), so the
// prompt never fires for an empty space and never contradicts the reap preview.
// With no terminal there is no one to ask, and silence is not consent: the space
// is kept and the caller is told where it is, because asking anyway would block
// on a pipe forever — a reap that hangs waiting for an answer nobody can give is
// worse than one that keeps the files. dir is the already-resolved space path.
func (r Real) consentToHeld(n Node, dir string) bool {
	if !tui.Interactive() {
		return false
	}
	return r.confirm(fmt.Sprintf("%s holds files in its held space.\n%s\nReap them?",
		tui.Accent(n.ID), tui.Muted("%s", dir)))
}

// rmMatches resolves the nodes a name reaps, git-style. The NODE id is the
// canonical handle, so it is tried first: an exact id is that one node even when
// it prefixes others, then a node-id prefix. Only when neither hits does a raw
// box INSTANCE name resolve as a FALLBACK (exact, then prefix) — the same handle
// `exec`/`run` accept, so a box booted by an older dabs or addressed by the name
// `ls` prints under a plain box is still reachable. A blank name matches nothing
// (never everything), and a prefix matching several nodes is REFUSED unless
// multiple is set, so a stray prefix cannot reap several nodes at once — the
// guard is the same whether the hits came from ids or instance names. A no-match
// returns no nodes and no error; the caller reports it and stops, so a reap of a
// name that isn't there is idempotent rather than a failure.
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
	// Fall back to the box instance name only when no node id matched, so the
	// canonical handle always wins over the record it turned out to be.
	if len(hits) == 0 {
		for _, n := range nodes {
			if n.Kind == KindBox && n.Instance == name {
				return []Node{n}, nil
			}
		}
		for _, n := range nodes {
			if n.Kind == KindBox && n.Instance != "" && strings.HasPrefix(n.Instance, name) {
				hits = append(hits, n)
			}
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
