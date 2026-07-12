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
	for _, key := range r.order {
		drv := r.drivers[key]
		var stop func()
		if key != "local" {
			stop = tui.Spinner(key)
		}
		infos, err := lsTimeout(drv, remoteTimeout)
		if stop != nil {
			stop()
		}
		if err != nil {
			fmt.Fprintln(os.Stdout, tui.Heading(fmt.Sprintf("%s (%s)", key, drv.Kind())))
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
	printNodeForest(nodes, state)

	// A box the driver holds that no node claims — booted by an older dabs, or by
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

// boxState is what a driver says about a box right now, and which driver said it.
// A box booted through a recipe `target:` lives on another driver, so where it is
// is part of what it is.
type boxState struct{ status, where string }

// printNodeForest writes every root and its descendants. A root is a node whose
// parent is not present — a project, or a node whose parent was reaped.
func printNodeForest(nodes []Node, state map[string]boxState) {
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
		fmt.Fprintf(os.Stdout, "%s%s%-9s %s\n", label, pad, string(n.Kind), nodeDetail(n, state))

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
		line := fmt.Sprintf("%s %s", tui.Muted("%s ·", n.Instance), tui.Status(st.status))
		if st.where != "local" {
			line += "  " + tui.Muted("%s", st.where)
		}
		return line
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
