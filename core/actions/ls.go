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
// A box's state comes from the driver; every other line comes from the node
// records. So a node whose box the driver has forgotten still shows — as gone,
// which is the fact worth seeing.
//
// Boxes on a server list under their own heading: a remote is a place too, just
// not this one. While a server is queried (an ssh round-trip) a spinner runs on
// stderr.
func (r Real) Ls(params.Ls) error {
	state := map[string]string{} // local instance -> status, from the driver
	var remote []string
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
			if key == "local" {
				state[in.Name] = in.Status
				continue
			}
			remote = append(remote, fmt.Sprintf("%-24s %s  %s", in.Name, tui.Status(in.Status), tui.Muted(key)))
		}
	}

	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	if len(nodes) == 0 && len(state) == 0 && len(remote) == 0 {
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
		if !claimed[inst] {
			loose = append(loose, fmt.Sprintf("%-24s %s", tui.Accent(inst), tui.Status(st)))
		}
	}
	sort.Strings(loose)
	if len(loose) > 0 {
		fmt.Fprintln(os.Stdout, tui.Heading("boxes with no node"))
		for _, l := range loose {
			fmt.Fprintln(os.Stdout, tui.Indent(l, 2))
		}
	}
	if len(remote) > 0 {
		fmt.Fprintln(os.Stdout, tui.Heading("elsewhere in the fleet"))
		for _, l := range remote {
			fmt.Fprintln(os.Stdout, tui.Indent(l, 2))
		}
	}
	return nil
}

// printNodeForest writes every root and its descendants. A root is a node whose
// parent is not present — a project, or a node whose parent was reaped.
func printNodeForest(nodes []Node, state map[string]string) {
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
func nodeDetail(n Node, state map[string]string) string {
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
		return fmt.Sprintf("%s %s", tui.Muted("%s ·", n.Instance), tui.Status(st))
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
