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
	// An ARCHIVED node is a box no driver holds any more. It is kept — what ran,
	// and from where, is the question a node exists to answer — but it is not what
	// you are looking at when you type `ls`, and it never goes away, so by default
	// it is not shown. Its SPACES are already gone: `down` reaps them.
	nodes := make([]Node, 0, len(all))
	archived := 0
	for _, n := range all {
		if n.Kind == KindBox {
			if _, live := state[n.Instance]; !live {
				archived++
				if !p.All {
					continue
				}
			}
		}
		nodes = append(nodes, n)
	}
	work := r.worktreeWork(nodes)
	if len(nodes) == 0 && len(state) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("nothing here yet"))
		return nil
	}
	defer func() {
		if archived > 0 && !p.All {
			fmt.Fprintln(os.Stdout, tui.Muted("\n%d archived (dabs ls --all)", archived))
		}
	}()

	// A box says where it runs, so its whole chain belongs under that heading —
	// the tree says WHAT, the heading says WHERE. A chain with a box on two
	// drivers appears under both: that is the fact, not a duplicate.
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	live := map[string][]Node{} // driver key -> the nodes in its chains
	var placed = map[string]bool{}
	livePlaced := map[string]bool{} // node id -> shown under some driver heading
	for _, n := range nodes {
		if n.Kind != KindBox {
			continue
		}
		st, up := state[n.Instance]
		if !up {
			continue
		}
		for _, a := range chainOf(n, byID) {
			livePlaced[a.ID] = true
			if !placed[st.where+"\x00"+a.ID] {
				placed[st.where+"\x00"+a.ID] = true
				live[st.where] = append(live[st.where], a)
			}
		}
	}

	// Local drivers only earn a heading when something is running on them. A
	// server always gets one — knowing a server is there and empty is the point.
	for _, key := range r.order {
		if len(live[key]) == 0 && !isServer(kinds[key]) {
			continue
		}
		fmt.Fprintln(os.Stdout, tui.Heading(header(key, kinds[key])))
		if len(live[key]) == 0 {
			fmt.Fprintln(os.Stdout, tui.Indent(tui.Muted("(nothing running)"), 2))
			continue
		}
		fmt.Fprint(os.Stdout, renderForest(r.viewNodes(live[key], state), lsColumns, 2))
	}

	// Everything dabs marked that is not running anywhere: the places, and the
	// boxes that are gone. Still yours — a worktree holds work, a volume holds
	// what a box left behind.
	var idle []Node
	seen := map[string]bool{}
	for _, n := range nodes {
		if anyLiveInChain(n, nodes, byID, state) {
			continue // already shown under the driver running it
		}
		// Carry the chain, so an idle box still hangs off the place it ran in
		// rather than floating as a root of its own — but an ancestor already
		// shown under a driver heading (a chain with both a live and a gone box)
		// is not repeated here.
		for _, a := range chainOf(n, byID) {
			if livePlaced[a.ID] {
				continue
			}
			if !seen[a.ID] {
				seen[a.ID] = true
				idle = append(idle, a)
			}
		}
	}
	if len(idle) > 0 {
		head := "no box"
		if anyWork(idle, work) {
			head += tui.Muted("   * has work you have not reviewed — dabs worktrees diff <name>")
		}
		fmt.Fprintln(os.Stdout, tui.Heading(head))
		fmt.Fprint(os.Stdout, renderForest(r.viewNodes(idle, state), lsColumns, 2))
	}

	// A box a driver holds that no node claims — booted by an older dabs, or by
	// hand. Still yours to reap, so still listed.
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
	for {
		up = append([]Node{n}, up...)
		p, ok := byID[n.Parent]
		if !ok {
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

// lsColumns are the columns `ls` draws for every node: the tree, its kind, the
// three space cells, its live/gone or merged/unmerged state, and where it is.
var lsColumns = []Column{ColNode, ColKind, ColVol, ColEph, ColTmp, ColState, ColWhere}

// tilde shortens a path under the home directory, so a tree of them reads at a
// glance.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return filepath.Join("~", strings.TrimPrefix(p, home))
}
