package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Ls renders what dabs owns as the tree it is: each node under the one it stacks
// on, with its kind and the place it marks.
//
//	dabs                project    ~/code/dabs
//	└─ dabs-a3f9c21d    worktree   branch dabs/a3f9c21d
//	   └─ claude-7c2d   box        claude-b1af95… · running
//
// A box's state comes from the driver that holds it — including a driver a recipe
// `target:` sent it to, whose name is shown beside the status. Every other line
// comes from the node records, so a node whose box no driver holds still shows,
// as gone: what ran and from where is the question a node answers.
//
// While a server is queried (an ssh round-trip) a spinner runs on stderr.
func (r Real) Ls(p params.Ls) error {
	// Every driver, not just the local one: a recipe with a `target:` boots its box
	// elsewhere, and a box the tree cannot find is a box the tree calls gone.
	state := map[string]boxState{}
	kinds := map[string]string{}
	for _, key := range r.order {
		drv := r.drivers[key]
		kinds[key] = drv.Kind()
		var stop func()
		if isServer(drv.Kind()) {
			stop = tui.Spinner(key)
		}
		infos, err := lsTimeout(drv, remoteTimeout)
		if stop != nil {
			stop()
		}
		if err != nil {
			fmt.Fprintln(os.Stdout, tui.Heading(header(key, drv.Kind())))
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Failure("%v", err), 2))
			continue
		}
		for _, in := range infos {
			state[in.Name] = boxState{status: in.Status, where: key}
		}
	}

	all, err := r.listNodes()
	if err != nil {
		return err
	}
	// Visibility follows LIFE, not history. Every boot mints a project marker for
	// the directory dabs ran from, so a plain listing fills with empty markers for
	// every dir dabs was ever run in. `ls` answers what is ALIVE: it shows only the
	// ACTIVE subtrees — a root and everything under it, holding a running box or
	// real files in some space. `ls --inactive` flips that, showing ONLY the
	// inactive ones (the empty records that remain), which `rm --inactive` sweeps.
	active, inactive := r.activeSubtrees(all, state)
	nodes := make([]Node, 0, len(all))
	for _, n := range all {
		if p.Inactive == active[n.ID] {
			continue // default: keep active; --inactive: keep inactive
		}
		nodes = append(nodes, n)
	}
	work := r.worktreeWork(nodes)
	if p.Inactive {
		if len(nodes) == 0 {
			fmt.Fprintln(os.Stdout, tui.Muted("no inactive subtrees"))
			return nil
		}
	} else if len(all) == 0 && len(state) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("nothing here yet"))
		return nil
	}
	defer func() {
		if len(inactive) > 0 && !p.Inactive {
			fmt.Fprintln(os.Stdout, tui.Muted("\n%d inactive (dabs ls --inactive)", len(inactive)))
		}
	}()

	// A box says where it runs, so its whole chain belongs under that heading —
	// the tree says WHAT, the heading says WHERE. A chain with a box on two
	// drivers appears under both: that is the fact, not a duplicate.
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	sections := map[string][]Node{}  // driver key -> the nodes under its heading
	placed := map[string]bool{}      // section+id, so one section never repeats a node
	shown := map[string]bool{}       // node id -> shown under some heading
	sectionOf := map[string]string{} // node id -> the first heading showing it
	add := func(key string, n Node) {
		if placed[key+"\x00"+n.ID] {
			return
		}
		placed[key+"\x00"+n.ID] = true
		sections[key] = append(sections[key], n)
		shown[n.ID] = true
		if _, ok := sectionOf[n.ID]; !ok {
			sectionOf[n.ID] = key
		}
	}
	for _, n := range nodes {
		if n.Kind != KindBox {
			continue
		}
		st, up := state[n.Instance]
		if !up {
			continue
		}
		for _, a := range chainOf(n, byID) {
			add(st.where, a)
		}
	}

	// A place with nothing running on it is still ON this machine — its path is
	// real here — so it belongs under the machine's own heading, chain and all,
	// not an error-looking bucket. A worktree there may hold an agent's
	// afternoon; a volume, what a box left behind.
	for _, n := range nodes {
		if n.Kind == KindBox || shown[n.ID] {
			continue
		}
		for _, a := range chainOf(n, byID) {
			if a.Kind != KindBox && !shown[a.ID] {
				add("local", a)
			}
		}
	}

	// A gone box is one tree with its place: whenever its parent is shown under
	// some heading, the box nests there — the same shape `rm` previews — instead
	// of floating as a parentless row.
	var orphans []Node // gone boxes whose parent is not shown at all
	for _, n := range nodes {
		if n.Kind != KindBox {
			continue
		}
		if _, up := state[n.Instance]; up {
			continue
		}
		if key, ok := sectionOf[n.Parent]; ok {
			add(key, n)
		} else {
			orphans = append(orphans, n)
		}
	}

	// Local drivers only earn a heading when something stands on this machine. A
	// server always gets one — knowing a server is there and empty is the point.
	for _, key := range r.order {
		if len(sections[key]) == 0 && !isServer(kinds[key]) {
			continue
		}
		head := header(key, kinds[key])
		if anyWork(sections[key], work) {
			head += tui.Muted("   * has work you have not reviewed — dabs worktrees diff <name>")
		}
		fmt.Fprintln(os.Stdout, tui.Heading(head))
		if len(sections[key]) == 0 {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Muted("(nothing running)"), 2))
			continue
		}
		fmt.Fprint(os.Stdout, renderForest(r.viewNodes(sections[key], state), lsColumns, 2))
	}

	// Gone boxes with no living parent context — their place record is gone —
	// have nowhere to nest, so they list flat under `no place`.
	if len(orphans) > 0 {
		fmt.Fprintln(os.Stdout, tui.Heading("no place"))
		fmt.Fprint(os.Stdout, renderForest(r.viewNodes(orphans, state), lsColumns, 2))
	}

	// A box a driver holds that no node claims — booted by an older dabs, or by
	// hand. Still yours to reap, so still listed. A live box is always active, so
	// `--inactive` (which shows only inactive subtrees) never lists these.
	// A live box is always active, so `--inactive` (only inactive subtrees) never
	// lists these.
	if !p.Inactive {
		claimed := map[string]bool{}
		for _, n := range nodes {
			if n.Instance != "" {
				claimed[n.Instance] = true
			}
		}
		var loose []string
		for inst, st := range state {
			if claimed[inst] {
				continue
			}
			line := fmt.Sprintf("%-26s %s", tui.Accent(inst), tui.Status(st.status))
			if st.where != "local" {
				line += "  " + tui.Muted("%s", st.where)
			}
			loose = append(loose, line)
		}
		sort.Strings(loose)
		if len(loose) > 0 {
			fmt.Fprintln(os.Stdout, tui.Heading("boxes with no node"))
			for _, l := range loose {
				fmt.Fprintln(os.Stdout, tui.Indent(l, 2))
			}
		}
	}
	return nil
}

// header names a place in the fleet: the target, and the driver that runs it.
func header(key, kind string) string {
	if key == "local" {
		return fmt.Sprintf("local (%s, this machine)", kind)
	}
	return fmt.Sprintf("%s (%s)", ellipsis(key, 28), kind)
}

// isServer reports whether a driver runs boxes somewhere else. A server is listed
// even when empty — that it exists and holds nothing is worth seeing.
func isServer(kind string) bool { return kind == "ssh" }

// ellipsis shortens a long target name (the internal nested-sandbox one runs to
// 40-odd characters) so a heading stays a heading.
func ellipsis(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// chainOf walks a node up to its root, returning the chain root-first — a box
// carries its places with it, so a heading shows where the CODE is too.
func chainOf(n Node, byID map[string]Node) []Node {
	var up []Node
	seen := map[string]bool{}
	for {
		up = append([]Node{n}, up...)
		seen[n.ID] = true
		p, ok := byID[n.Parent]
		if !ok || seen[p.ID] {
			// No parent, or the parent closes a cycle (a self-parent, or a
			// corrupt record chain): stop rather than walk it forever.
			return up
		}
		n = p
	}
}

// anyLiveInChain reports whether a node has a live box anywhere below it, so a
// place already shown under a driver heading is not repeated as idle.
func anyLiveInChain(n Node, nodes []Node, byID map[string]Node, state map[string]boxState) bool {
	for _, c := range nodes {
		if c.Kind != KindBox {
			continue
		}
		if _, up := state[c.Instance]; !up {
			continue
		}
		for _, a := range chainOf(c, byID) {
			if a.ID == n.ID {
				return true
			}
		}
	}
	return false
}

// activeSubtrees marks every ACTIVE node and names the tops of the inactive ones.
// Life is judged per NODE, and activity propagates UP, never DOWN: a node is
// active iff it is self-active or holds a self-active descendant, so a live box
// keeps its whole line of ancestors visible while a gone, empty box stays dead
// weight even under a living parent. `ls` hides an inactive node by default, `ls
// --inactive` shows it, and `rm --inactive` sweeps it.
//
// active names every active node's id. inactiveRoots names the TOP of each
// inactive subtree — an inactive node whose parent is active or absent — the id
// `rm --inactive` reaps to take just that dead branch without disturbing the
// living tree above it (an inactive node with an inactive parent is reaped as part
// of that parent's branch, not on its own).
func (r Real) activeSubtrees(nodes []Node, state map[string]boxState) (active map[string]bool, inactiveRoots []string) {
	byID := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	active = make(map[string]bool, len(nodes))
	// A self-active node lights its whole line of ancestors: walk up from each one,
	// guarding a corrupt cycle, and mark every node on the way to the root.
	for _, n := range nodes {
		if !r.nodeSelfActive(n, state) {
			continue
		}
		cur := n
		seen := map[string]bool{}
		for !seen[cur.ID] {
			seen[cur.ID] = true
			active[cur.ID] = true
			p, ok := byID[cur.Parent]
			if !ok {
				break
			}
			cur = p
		}
	}
	// An inactive node whose parent is active (or gone) is the top of a dead branch;
	// one whose parent is also inactive is reaped with that parent, not on its own.
	for _, n := range nodes {
		if active[n.ID] {
			continue
		}
		if p, ok := byID[n.Parent]; ok && !active[p.ID] {
			continue
		}
		inactiveRoots = append(inactiveRoots, n.ID)
	}
	return active, inactiveRoots
}

// nodeSelfActive reports one node's OWN claim to life: a running box, or a node
// any of whose three spaces holds a real file. It is the atom activeSubtrees is
// built from. spaceHolds is the single content predicate — the same one the ls
// space cells and the rm consent consult — so "holds files" means exactly the
// same thing here as everywhere, and a tree of only empty directories holds
// nothing.
func (r Real) nodeSelfActive(n Node, state map[string]boxState) bool {
	if n.Kind == KindBox {
		if _, live := state[n.Instance]; live {
			return true
		}
	}
	for _, dir := range r.nodeSpaceDirs(n) {
		if holds, err := r.spaceHolds(dir); err == nil && holds {
			return true
		}
	}
	return false
}

// nodeSpaceDirs is a node's three space directories — volume, held (resolved
// through its legacy ephemeral/ fallback so an older node's files still count),
// and tmp.
func (r Real) nodeSpaceDirs(n Node) []string {
	var dirs []string
	if d, err := r.resolveNodeSpace(n.ID, SpaceVolume); err == nil {
		dirs = append(dirs, d)
	}
	if d, err := r.resolveHeldSpace(n.ID); err == nil {
		dirs = append(dirs, d)
	}
	if d, err := r.resolveNodeSpace(n.ID, SpaceTmp); err == nil {
		dirs = append(dirs, d)
	}
	return dirs
}

// worktreeWork marks the worktree nodes holding something a reap would destroy:
// uncommitted changes, or commits no other branch has. It is the same question
// `dabs worktrees` answers with HAS WORK — asked here so a tree of places cannot
// read as a tree of things that do not matter.
func (r Real) worktreeWork(nodes []Node) map[string]bool {
	work := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != KindWorktree {
			continue
		}
		path, err := r.resolveNodeData(n.ID)
		if err != nil {
			continue
		}
		_, dirty, ahead, err := r.data.GitState(path)
		if err != nil {
			continue
		}
		work[n.ID] = dirty || ahead > 0
	}
	return work
}

// anyWork reports whether any node listed holds unreviewed work.
func anyWork(nodes []Node, work map[string]bool) bool {
	for _, n := range nodes {
		if work[n.ID] {
			return true
		}
	}
	return false
}

// boxState is what a driver says about a box right now, and which driver said it.
// A box booted through a recipe `target:` lives on another driver, so where it is
// is part of what it is.
type boxState struct{ status, where string }

// boxStates asks every driver in the fleet which boxes it holds — the same
// query `ls` runs — keyed by instance name. Any view built from it agrees with
// `ls` about which boxes are live; a driver that errors contributes nothing,
// so its boxes read as gone rather than failing the caller.
func (r Real) boxStates() map[string]boxState {
	state := map[string]boxState{}
	for _, key := range r.order {
		infos, err := lsTimeout(r.drivers[key], remoteTimeout)
		if err != nil {
			continue
		}
		for _, in := range infos {
			state[in.Name] = boxState{status: in.Status, where: key}
		}
	}
	return state
}

// lsColumns are the columns `ls` draws for every node: the tree, its kind, the
// three space cells, its live/gone or merged/unmerged state, and where it is.
var lsColumns = []Column{ColNode, ColKind, ColVol, ColHeld, ColTmp, ColState, ColWhere}

// tilde shortens a path under the home directory, so a tree of them reads at a
// glance.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return filepath.Join("~", strings.TrimPrefix(p, home))
}
