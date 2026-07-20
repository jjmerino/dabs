package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// localSection is the one bucket every local driver's nodes render in — the
// flat, heading-less tree at the top of `dabs ls`.
const localSection = "local"

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
	complete := true
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
			// A driver that cannot answer (missing bwrap, a stopped docker
			// daemon) does not kill the listing: warn ONCE on stderr — one
			// concise line carrying the driver's own message (the install
			// hint) — and render the tree anyway, with each unconfirmed box's
			// state degraded (see markStateUnknown). A server keeps its
			// heading: that it is registered and unreachable is the finding.
			complete = false
			if !isServer(drv.Kind()) {
				fmt.Fprintln(os.Stderr, tui.Warn("dabs: %s unavailable: %v", key, err))
				continue
			}
			fmt.Fprintln(os.Stdout, tui.Heading(header(key, drv.Kind())))
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Failure("%v", err), 2))
			continue
		}
		for _, in := range infos {
			state[in.Name] = boxState{status: in.Status, where: key, kind: drv.Kind()}
		}
	}

	all, err := r.listNodes()
	if err != nil {
		return err
	}
	// Visibility follows LIFE, not history. Every boot mints a project marker for
	// the directory dabs ran from, so a plain listing fills with empty markers for
	// every dir dabs was ever run in. `ls` answers what is ALIVE: it shows only the
	// ACTIVE subtrees — a root and everything under it, holding a running box,
	// real files in some space, or an unmerged externally-managed worktree of a
	// project's repo. `ls --inactive` flips that, showing ONLY the
	// inactive ones (the empty records that remain), which `rm --inactive` sweeps.
	// Foreign worktrees — checkouts git's registry knows that dabs does not
	// own — computed once for the whole listing, keyed by project id. Only the
	// UNMERGED ones (dirty, or carrying commits the base does not have) are
	// kept: a clean, fully-merged foreign worktree is finished work. The map
	// both renders (attached under each project's view) and counts as LIFE in
	// activeSubtrees: a project with one is active.
	foreign := r.foreignWorktrees(all)
	active, inactive := r.activeSubtrees(all, state, complete, foreign)
	nodes := make([]Node, 0, len(all))
	for _, n := range all {
		if p.Inactive == active[n.ID] {
			continue // default: keep active; --inactive: keep inactive
		}
		nodes = append(nodes, n)
	}
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

	// A box says where it runs. Every LOCAL driver (apple, docker) collapses
	// into ONE flat tree — the section keyed localSection, drawn with no
	// heading — because a place is on THIS machine whichever local driver a box
	// on it uses. A remote server keeps its own section, keyed by its name. A
	// chain with a box locally AND on a server appears in both: that is the
	// fact, not a duplicate.
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	// sectionKey folds every local driver into one flat section and leaves each
	// server on its own.
	sectionKey := func(where string) string {
		if isServer(kinds[where]) {
			return where
		}
		return localSection
	}
	sections := map[string][]Node{}  // section key -> the nodes it renders
	placed := map[string]bool{}      // section+id, so one section never repeats a node
	shown := map[string]bool{}       // node id -> shown in some section
	sectionOf := map[string]string{} // node id -> the first section showing it
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
			add(sectionKey(st.where), a)
		}
	}

	// A place with nothing running on it is still ON this machine — its path is
	// real here — so it belongs in the flat local tree, chain and all, not an
	// error-looking bucket. A worktree there may hold an agent's afternoon; a
	// volume, what a box left behind.
	for _, n := range nodes {
		if n.Kind == KindBox || shown[n.ID] {
			continue
		}
		for _, a := range chainOf(n, byID) {
			if a.Kind != KindBox && !shown[a.ID] {
				add(localSection, a)
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

	// draw renders one section's forest: the box driver tags, the git-state
	// degradation, and the foreign-worktree rows all hang on the same view trees.
	draw := func(key string, indent int) {
		views := r.viewNodes(sections[key], state)
		if !complete {
			markStateUnknown(views)
		}
		attachForeignWorktrees(views, foreign)
		fmt.Fprint(os.Stdout, renderForest(views, lsColumns, indent))
	}

	// The flat local tree draws first, with NO heading — every local driver's
	// boxes and every on-machine place live here as one tree.
	if len(sections[localSection]) > 0 {
		draw(localSection, 2)
	}

	// A server always earns a heading — knowing a server is there and empty is
	// the point — with its own section drawn beneath it.
	for _, key := range r.order {
		if !isServer(kinds[key]) {
			continue
		}
		fmt.Fprintln(os.Stdout, tui.Heading(header(key, kinds[key])))
		if len(sections[key]) == 0 {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Muted("(nothing running)"), 2))
			continue
		}
		draw(key, 2)
	}

	// Gone boxes with no living parent context — their place record is gone —
	// have nowhere to nest, so they list flat under `no place`.
	if len(orphans) > 0 {
		fmt.Fprintln(os.Stdout, tui.Heading("no place"))
		views := r.viewNodes(orphans, state)
		if !complete {
			markStateUnknown(views)
		}
		fmt.Fprint(os.Stdout, renderForest(views, lsColumns, 2))
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
// active iff it holds life itself — self-active, or a project whose repo has an
// unmerged externally-managed worktree — or has a descendant that does, so a live box
// keeps its whole line of ancestors visible while a gone, empty box stays dead
// weight even under a living parent. `ls` hides an inactive node by default, `ls
// --inactive` shows it, and `rm --inactive` sweeps it.
//
// active names every active node's id. inactiveRoots names the TOP of each
// inactive subtree — an inactive node whose parent is active or absent — the id
// `rm --inactive` reaps to take just that dead branch without disturbing the
// living tree above it (an inactive node with an inactive parent is reaped as part
// of that parent's branch, not on its own).
// complete is the drivers' answer quality: with an INCOMPLETE answer (a driver
// missing or unreachable), a box absent from state is unconfirmed, not proven
// dead — it counts as potentially active, so it stays in the default listing
// (marked `(error: no driver)`) instead of vanishing into --inactive, which is
// for confirmed-dead history.
// foreign is the listing's one enumeration of unmerged externally-managed
// worktrees (foreignWorktrees), keyed by project id: a project whose repo has
// one is ACTIVE — the unmerged checkout is life the listing must not hide.
func (r Real) activeSubtrees(nodes []Node, state map[string]boxState, complete bool, foreign map[string][]*NodeView) (active map[string]bool, inactiveRoots []string) {
	byID := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	active = make(map[string]bool, len(nodes))
	// A self-active node lights its whole line of ancestors: walk up from each one,
	// guarding a corrupt cycle, and mark every node on the way to the root.
	for _, n := range nodes {
		if !r.nodeSelfActive(n, state, complete) && len(foreign[n.ID]) == 0 {
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
func (r Real) nodeSelfActive(n Node, state map[string]boxState, complete bool) bool {
	if n.Kind == KindBox {
		if _, live := state[n.Instance]; live || !complete {
			// An incomplete drivers' answer cannot prove any box dead, so an
			// unconfirmed box counts as potentially active.
			return true
		}
	}
	for _, dir := range r.nodeSpaceDirs(n) {
		if holds, err := r.spaceHolds(dir); err == nil && holds {
			return true
		}
	}
	// An externally-managed worktree marker (kind worktree, Dir the checkout,
	// no held checkout of its own) holds life when the checkout it stands on
	// has unmerged work — the same judgment every worktree row gets. A checkout
	// git cannot answer for holds nothing dabs can show, so it reads inactive
	// and stays sweepable.
	if n.Kind == KindWorktree && n.Worktree == nil && n.Dir != "" {
		if _, dirty, ahead, err := r.data.GitState(n.Dir); err == nil &&
			r.worktreeJudgment(n.Dir, dirty, ahead) != CellNoDiff {
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

// boxState is what a driver says about a box right now, which driver said it
// (the registry key), and that driver's kind (apple/docker/ssh). A box booted
// through a recipe `target:` lives on another driver, so where it is is part of
// what it is; the kind is what the KIND column tags a box with.
type boxState struct{ status, where, kind string }

// driversAnswer is one query of every driver: which instances they hold, and
// whether every driver actually answered. An instance absent from a COMPLETE
// answer is gone; absent from an incomplete one, it is merely unconfirmed.
type driversAnswer struct {
	state    map[string]boxState
	complete bool
}

// boxStates asks every driver which boxes it holds — the same query `ls`
// runs — keyed by instance name. Any view built from it agrees with `ls`
// about which boxes are live; a driver that errors contributes nothing, so
// its boxes read as gone rather than failing the caller — and the answer is
// marked incomplete, so a reap does not treat that silence as absence.
func (r Real) boxStates() driversAnswer {
	ans := driversAnswer{state: map[string]boxState{}, complete: true}
	for _, key := range r.order {
		infos, err := lsTimeout(r.drivers[key], remoteTimeout)
		if err != nil {
			ans.complete = false
			continue
		}
		for _, in := range infos {
			ans.state[in.Name] = boxState{status: in.Status, where: key, kind: r.drivers[key].Kind()}
		}
	}
	return ans
}

// lsColumns are the columns `ls` draws for every node: the tree, its kind, the
// three space cells, its state (live/gone, or a worktree's judgment and git
// signal), and the INFO cell folding its working location or shell-in command.
var lsColumns = []Column{ColNode, ColKind, ColVol, ColHeld, ColTmp, ColState, ColInfo}

// tilde shortens a path under the home directory, so a tree of them reads at a
// glance.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return filepath.Join("~", strings.TrimPrefix(p, home))
}

// unmanagedID marks a foreign worktree row. It is a display marker, not a node
// id: rm/cd/exec resolve node records only, so nothing resolves it.
const unmanagedID = "(unmanaged)"

// foreignWorktrees computes, for EVERY project node, the display rows of its
// repo's worktrees that dabs does not own — cut by git directly or by another
// tool — keyed by project id. Only UNMERGED ones earn a row: dirty, carrying
// commits whose content the base does not have, or a state git cannot answer
// for (never report finished what cannot be shown). A clean, fully-merged
// checkout is finished work — no row. The rows are display-only: NODE reads
// `(unmanaged)`, the spaces are empty (they have none), and STATE is the same
// git judgment dabs's own worktree rows get. A repo git cannot enumerate (no
// git binary, not a repo) contributes no rows.
func (r Real) foreignWorktrees(all []Node) map[string][]*NodeView {
	// Two project nodes can stand over the SAME repository — one on the main
	// checkout, one minted by running dabs from inside a linked worktree —
	// and git answers `worktree list` with the one shared registry from any
	// checkout. Projects are grouped by the repo's common .git dir, and each
	// repo enumerates ONCE, its rows attached to a single project: the one
	// whose dir IS the main checkout when one is tracked, else the oldest
	// node. Only the representative's OWN dir is excluded from its rows (a
	// project must not list itself); a linked worktree that some OTHER
	// project stands on still renders — an unmerged checkout is exactly what
	// the listing must never hide.
	type repoGroup struct {
		rep     Node // the project the repo's rows attach to
		repMain bool // rep's dir is the main checkout
	}
	// Grouping and the main-checkout comparison run on CANONICAL paths: two
	// projects reaching one repo through differing symlinked paths must land
	// in the same group, or the registry enumerates twice.
	groups := map[string]*repoGroup{}
	for _, n := range all {
		if n.Kind != KindProject || n.Dir == "" {
			continue
		}
		dir := r.canonPath(n.Dir)
		key := dir
		main := false
		if cd, err := r.data.GitCommonDir(n.Dir); err == nil {
			key = r.canonPath(cd)
			main = filepath.Dir(key) == dir // the common dir is <main checkout>/.git
		}
		g := groups[key]
		if g == nil {
			groups[key] = &repoGroup{rep: n, repMain: main}
			continue
		}
		if (main && !g.repMain) || (main == g.repMain && n.Created < g.rep.Created) {
			g.rep, g.repMain = n, main
		}
	}

	out := map[string][]*NodeView{}
	var owned map[string]bool // built on the first repo that needs it
	for _, g := range groups {
		paths, err := r.data.GitListWorktrees(g.rep.Dir)
		if err != nil {
			continue
		}
		if owned == nil {
			owned = r.ownedWorktreePaths(all)
		}
		for _, p := range paths {
			cp := filepath.Clean(p)
			if owned[cp] || cp == filepath.Clean(g.rep.Dir) {
				continue
			}
			st := CellNA
			if _, dirty, ahead, gerr := r.data.GitState(p); gerr == nil {
				st = r.worktreeJudgment(p, dirty, ahead)
			}
			if st == CellNoDiff {
				continue
			}
			out[g.rep.ID] = append(out[g.rep.ID], &NodeView{
				ID:    unmanagedID,
				Kind:  KindWorktree,
				Info:  tilde(p),
				State: st,
			})
		}
	}
	return out
}

// attachForeignWorktrees hangs each project's precomputed foreign rows under
// its view, wherever in the forest that project renders.
func attachForeignWorktrees(views []*NodeView, foreign map[string][]*NodeView) {
	var walk func(v *NodeView)
	walk = func(v *NodeView) {
		for _, c := range v.Children {
			walk(c)
		}
		v.Children = append(v.Children, foreign[v.ID]...)
	}
	for _, v := range views {
		walk(v)
	}
}

// workingDir is the real place a node marks, as `dabs cd` resolves it: a
// project's source repo, a worktree's checkout. A box (or anything with no
// working place of its own) returns "" — its place IS its node dir.
func (r Real) workingDir(n Node) string {
	switch n.Kind {
	case KindProject:
		return n.Dir
	case KindWorktree:
		if n.Worktree != nil {
			if d, err := r.resolveNodeData(n.ID); err == nil {
				return d
			}
			return ""
		}
		return n.Dir
	default:
		return ""
	}
}

// gitSignal renders a directory's git state as a compact prompt string, or ""
// when the directory is not a git repository. It routes through the data seam,
// so it is exercised against the fake.
func (r Real) gitSignal(dir string) string {
	if dir == "" {
		return ""
	}
	p, err := r.data.GitPromptStatus(dir)
	if err != nil {
		return ""
	}
	return formatGitSignal(p)
}

// formatGitSignal draws a GitPrompt like a zsh git prompt: the branch, then
// `+` staged, `*` unstaged, `%` untracked, and ⇡N/⇣N ahead/behind. A clean
// branch with no divergence is just its name.
func formatGitSignal(p data.GitPrompt) string {
	if p.Branch == "" {
		return ""
	}
	var flags strings.Builder
	if p.Staged {
		flags.WriteString("+")
	}
	if p.Unstaged {
		flags.WriteString("*")
	}
	if p.Untracked {
		flags.WriteString("%")
	}
	if p.Ahead > 0 {
		fmt.Fprintf(&flags, "⇡%d", p.Ahead)
	}
	if p.Behind > 0 {
		fmt.Fprintf(&flags, "⇣%d", p.Behind)
	}
	if flags.Len() == 0 {
		return p.Branch
	}
	return p.Branch + " " + flags.String()
}

// canonPath resolves a path's symlinks for identity comparison, falling back
// to the lexical Clean when it cannot be resolved (absent, permission).
func (r Real) canonPath(p string) string {
	if resolved, err := r.data.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

// ownedWorktreePaths maps the checkout path of every worktree node dabs
// tracks, so a foreign listing can skip them: the checkouts dabs cut (they
// live in the node's held space) and the externally-managed checkouts dabs
// ran from and marked (their Dir) — each already has a node row of its own.
func (r Real) ownedWorktreePaths(nodes []Node) map[string]bool {
	owned := map[string]bool{}
	for _, n := range nodes {
		switch {
		case n.Worktree != nil:
			if p, err := r.resolveNodeData(n.ID); err == nil {
				owned[filepath.Clean(p)] = true
			}
		case n.Kind == KindWorktree && n.Dir != "":
			owned[filepath.Clean(n.Dir)] = true
		}
	}
	return owned
}
