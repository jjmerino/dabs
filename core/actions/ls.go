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
func (r Real) Ls(params.Ls) error {
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

	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	if len(nodes) == 0 && len(state) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("nothing here yet"))
		return nil
	}

	// A box says where it runs, so its whole chain belongs under that heading —
	// the tree says WHAT, the heading says WHERE. A chain with a box on two
	// drivers appears under both: that is the fact, not a duplicate.
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	live := map[string][]Node{} // driver key -> the nodes in its chains
	var placed = map[string]bool{}
	for _, n := range nodes {
		if n.Kind != KindBox {
			continue
		}
		st, up := state[n.Instance]
		if !up {
			continue
		}
		for _, a := range chainOf(n, byID) {
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
		printNodeForest(live[key], state, 2)
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
		// rather than floating as a root of its own.
		for _, a := range chainOf(n, byID) {
			if !seen[a.ID] {
				seen[a.ID] = true
				idle = append(idle, a)
			}
		}
	}
	if len(idle) > 0 {
		fmt.Fprintln(os.Stdout, tui.Heading("nothing running here"))
		printNodeForest(idle, state, 2)
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

// boxState is what a driver says about a box right now, and which driver said it.
// A box booted through a recipe `target:` lives on another driver, so where it is
// is part of what it is.
type boxState struct{ status, where string }

// printNodeForest writes every root and its descendants. A root is a node whose
// parent is not present — a project, or a node whose parent was reaped.
func printNodeForest(nodes []Node, state map[string]boxState, indent int) {
	byID := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	kids := map[string][]Node{}
	var roots []Node
	for _, n := range nodes {
		if _, ok := byID[n.Parent]; n.Parent != "" && ok {
			kids[n.Parent] = append(kids[n.Parent], n)
			continue
		}
		roots = append(roots, n)
	}
	oldestFirst := func(ns []Node) {
		sort.SliceStable(ns, func(i, j int) bool { return ns[i].Created < ns[j].Created })
	}
	oldestFirst(roots)
	for _, ns := range kids {
		oldestFirst(ns)
	}

	// One column width for the whole forest, so kind and detail line up however
	// deep a node sits.
	width := 0
	var measure func(n Node, depth int)
	measure = func(n Node, depth int) {
		if w := depth*3 + len([]rune(n.ID)); w > width {
			width = w
		}
		for _, k := range kids[n.ID] {
			measure(k, depth+1)
		}
	}
	for _, n := range roots {
		measure(n, 0)
	}

	var walk func(n Node, prefix string, last bool, depth int)
	walk = func(n Node, prefix string, last bool, depth int) {
		stem := ""
		if depth > 0 {
			stem = "├─ "
			if last {
				stem = "└─ "
			}
		}
		label := prefix + stem + n.ID
		pad := strings.Repeat(" ", maxInt(1, width+2-len([]rune(label))))
		fmt.Fprintf(os.Stdout, "%s%s%s%-9s %s\n", strings.Repeat(" ", indent), label, pad, string(n.Kind), nodeDetail(n, state))

		next := prefix
		if depth > 0 {
			if last {
				next += "   "
			} else {
				next += "│  "
			}
		}
		ks := kids[n.ID]
		for i, k := range ks {
			walk(k, next, i == len(ks)-1, depth+1)
		}
	}
	for _, n := range roots {
		walk(n, "", true, 0)
	}
}

// nodeDetail says what a node is right now: where a project points, which branch
// a worktree carries, whether a box is still running.
func nodeDetail(n Node, state map[string]boxState) string {
	switch n.Kind {
	case KindProject, KindWorkdir:
		return tui.Muted("%s", tilde(n.Dir))
	case KindWorktree:
		if n.Worktree != nil {
			return tui.Muted("branch %s", n.Worktree.Branch)
		}
	case KindBox:
		st, up := state[n.Instance]
		if !up {
			return tui.Muted("%s · gone", n.Instance)
		}
		return fmt.Sprintf("%s %s", tui.Muted("%s ·", n.Instance), tui.Status(st.status))
	}
	return tui.Muted("%s", n.Recipe)
}

// tilde shortens a path under the home directory, so a tree of them reads at a
// glance.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return filepath.Join("~", strings.TrimPrefix(p, home))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
