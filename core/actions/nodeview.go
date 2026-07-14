package actions

// A NodeView is what a node's state LOOKS LIKE, computed once and drawn many
// ways. `ls` and `rm` both need to show a node and what stands on it, and they
// must agree about what "empty", "held" and "gone" mean. So the state is
// resolved in one place — viewNodes — into a tree of view-models, and a
// renderer draws that tree without ever touching the filesystem or a driver
// again. The split is the point: deciding is separate from drawing.

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jjmerino/dabs/core/tui"
)

// Cell is one column's resolved display state.
type Cell int

const (
	CellNA       Cell = iota // "—"  not applicable to this node kind
	CellEmpty                // ""   space present, holds nothing (safe to reap)
	CellHolds                // "●"  space holds files a reap would lose
	CellLive                 // box:      running
	CellGone                 // box:      gone (no live instance)
	CellNoDiff               // worktree: no unreviewed work
	CellUnmerged             // worktree: commits ahead of the base branch
	CellHasWork              // worktree: uncommitted/untracked work, nothing ahead
)

// Symbol is the plain glyph or word a cell shows. Piped output keeps it, so a
// script reading the columns sees the same tokens a terminal draws.
func (c Cell) Symbol() string {
	switch c {
	case CellEmpty:
		return ""
	case CellHolds:
		return "●"
	case CellLive:
		return "live"
	case CellGone:
		return "gone"
	case CellNoDiff:
		return "no-diff"
	case CellUnmerged:
		return "unmerged"
	case CellHasWork:
		return "has work"
	default:
		return "—"
	}
}

func (c Cell) String() string { return c.Symbol() }

// NodeView is a display-ready snapshot of a node and its subtree — a TRUE tree,
// computed once, so any renderer consumes it with no further fs/driver call.
type NodeView struct {
	ID       string
	Kind     NodeKind
	Where    string // the "where": cwd (project) / on-disk folder (workdir, worktree) / instance (box)
	Volume   Cell   // CellEmpty / CellHolds
	Held     Cell
	Tmp      Cell
	State    Cell // box: live/gone · worktree: no-diff/has work/unmerged · else CellNA
	Children []*NodeView
}

// viewNodes turns a SET of nodes into view trees. It is the ONE place node
// state becomes a view. It reads each node's OWN spaces (local stat, fast) and,
// for a box, its liveness from the caller's pre-fetched `state` map — NEVER a
// fresh driver/server query. So building a view for a set that omits some
// server's box never contacts that server. Nodes whose parent is not in the set
// are roots.
func (r Real) viewNodes(nodes []Node, state map[string]boxState) []*NodeView {
	views := make(map[string]*NodeView, len(nodes))
	created := make(map[string]string, len(nodes))
	inSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		views[n.ID] = r.viewNode(n, state)
		created[n.ID] = n.Created
		inSet[n.ID] = true
	}

	var roots []*NodeView
	for _, n := range nodes {
		v := views[n.ID]
		if n.Parent != "" && inSet[n.Parent] {
			p := views[n.Parent]
			p.Children = append(p.Children, v)
			continue
		}
		roots = append(roots, v)
	}

	// Oldest-first everywhere, so a listing keeps a stable order however the
	// records come off disk.
	oldest := func(vs []*NodeView) {
		sort.SliceStable(vs, func(i, j int) bool { return created[vs[i].ID] < created[vs[j].ID] })
	}
	oldest(roots)
	for _, v := range views {
		oldest(v.Children)
	}
	return roots
}

// viewNode resolves one node's cells. Spaces are always local; box liveness
// comes only from the passed map; worktree state comes from local git.
func (r Real) viewNode(n Node, state map[string]boxState) *NodeView {
	v := &NodeView{
		ID:     n.ID,
		Kind:   n.Kind,
		Volume: r.spaceCell(n.ID, SpaceVolume),
		Held:   r.heldCell(n.ID),
		Tmp:    r.spaceCell(n.ID, SpaceTmp),
		State:  CellNA,
	}
	switch n.Kind {
	case KindBox:
		// A box marks a sandbox, but its bytes still live somewhere: the node dir
		// under ~/.dabs/nodes/<id> holds its volume/held/tmp spaces. WHERE
		// points there (tilde-abbreviated) so a box's on-disk location is as
		// discoverable as a place's; the driver INSTANCE name — the running box —
		// rides alongside so users still see it and rm/exec still resolve it.
		where := tilde(r.boxNodeDir(n))
		if n.Instance != "" {
			where += "  (" + n.Instance + ")"
		}
		v.Where = where
		if _, live := state[n.Instance]; live {
			v.State = CellLive
		} else {
			v.State = CellGone
		}
	case KindWorktree:
		v.Where = tilde(r.nodeFolder(n))
		v.State = r.worktreeState(n)
	case KindWorkdir:
		v.Where = tilde(r.nodeFolder(n))
	default: // project
		v.Where = tilde(n.Dir)
	}
	return v
}

// nodeFolder is where a node's working bytes actually live on disk — a
// worktree's checkout, a workdir's copy — resolved from the node's own storage
// rather than a recorded source path, which for a workdir is its parent project.
// It falls back to the recorded Dir (a live mount's target has no node-local
// copy).
func (r Real) nodeFolder(n Node) string {
	if data, err := r.resolveNodeData(n.ID); err == nil && r.dataExists(data) {
		return data
	}
	if held, err := r.resolveHeldSpace(n.ID); err == nil {
		if work := filepath.Join(held, "work"); r.dataExists(work) {
			return work
		}
	}
	return n.Dir
}

// boxNodeDir is the directory a box node's bytes live in — its own node dir,
// which holds the volume/held/tmp spaces. It falls back to the node id when
// the dir cannot be resolved, so a box row always says something locatable.
func (r Real) boxNodeDir(n Node) string {
	if dir, err := r.resolveNodeDir(n.ID); err == nil {
		return dir
	}
	return n.ID
}

// spaceCell reports whether a node's own space holds anything. A resolve error
// reads as empty: one unreadable node must not crash a whole listing.
func (r Real) spaceCell(id, space string) Cell {
	dir, err := r.resolveNodeSpace(id, space)
	if err != nil {
		return CellEmpty
	}
	holds, err := r.spaceHolds(dir)
	if err != nil || !holds {
		return CellEmpty
	}
	return CellHolds
}

// heldCell reports whether a node's held space holds anything, reading the
// current held/ dir OR a legacy ephemeral/ one an older dabs wrote — so a legacy
// node still shows the ● that guards its files.
func (r Real) heldCell(id string) Cell {
	dir, err := r.resolveHeldSpace(id)
	if err != nil {
		return CellEmpty
	}
	holds, err := r.spaceHolds(dir)
	if err != nil || !holds {
		return CellEmpty
	}
	return CellHolds
}

// worktreeState reads a worktree's git state into the SAME three-value
// vocabulary `worktrees ls` prints (see worktreeJudgment). A git error leaves
// the state unknown rather than guessing.
func (r Real) worktreeState(n Node) Cell {
	path, err := r.resolveNodeData(n.ID)
	if err != nil {
		return CellNA
	}
	_, dirty, ahead, err := r.data.GitState(path)
	if err != nil {
		return CellNA
	}
	return r.worktreeJudgment(path, dirty, ahead)
}

// worktreeJudgment classifies a worktree the ONE way every verb reports it —
// `ls`, `worktrees ls`, and the rm guard all call this, so they cannot
// disagree. UNMERGED means commits ahead whose CONTENT the base does not have:
// a squash merge lands the bytes while leaving the commits ahead, and landed
// work is reviewed work, not something a reap would lose. HAS WORK is
// uncommitted/untracked changes; NO-DIFF is everything else, including a
// squash-merged branch.
func (r Real) worktreeJudgment(path string, dirty bool, ahead int) Cell {
	if ahead > 0 && r.aheadCarriesContent(path) {
		return CellUnmerged
	}
	if dirty {
		return CellHasWork
	}
	return CellNoDiff
}

// aheadCarriesContent reports whether the worktree's commits hold content the
// base does not — GitLanded's question, inverted. An error reads as carrying
// content: never report reviewed what cannot be shown.
func (r Real) aheadCarriesContent(path string) bool {
	landed, err := r.data.GitLanded(path)
	if err != nil {
		return true
	}
	return !landed
}

// Column names a drawable field. A renderer is told which to draw and in what
// order, so `rm`'s preview and `ls` share one renderer and differ only in the
// columns they ask for.
type Column int

const (
	ColNode Column = iota
	ColKind
	ColVol
	ColHeld
	ColTmp
	ColState
	ColWhere
)

// columnTitle is the header label for a column.
func columnTitle(c Column) string {
	switch c {
	case ColKind:
		return "KIND"
	case ColVol:
		return "VOL"
	case ColHeld:
		return "HELD"
	case ColTmp:
		return "TMP"
	case ColState:
		return "STATE"
	case ColWhere:
		return "WHERE"
	default:
		return "NODE"
	}
}

// sanitizeCell neutralizes an untrusted display value — a node id or a WHERE
// path, either of which can carry whatever a filesystem name or a hand-written
// node record holds. It drops ASCII control bytes (< 0x20 and 0x7F), which
// removes the ESC that begins any ANSI escape sequence (leaving its inert
// letters as plain text) and the newline that would split one row into phantom
// tree lines. It must run on the RAW value, before dabs wraps it in its own
// styling escapes, so those intentional codes are kept.
func sanitizeCell(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// renderForest draws view trees in the nested ├─/└─ style, aligning exactly the
// columns requested. Column widths are computed across the whole forest so deep
// nodes still line up. The result ends with a trailing newline.
func renderForest(roots []*NodeView, cols []Column, indent int) string {
	type row struct {
		v     *NodeView
		label string // the ColNode cell: tree prefix + id, plain (for measuring)
	}
	var rows []row

	// Walk the tree once, building each row's node-column label with its branch
	// glyphs. The rest of a row's cells are read from the view when we draw.
	var walk func(v *NodeView, prefix string, last bool, depth int)
	walk = func(v *NodeView, prefix string, last bool, depth int) {
		stem := ""
		if depth > 0 {
			stem = "├─ "
			if last {
				stem = "└─ "
			}
		}
		rows = append(rows, row{v: v, label: prefix + stem + v.ID})
		next := prefix
		if depth > 0 {
			if last {
				next += "   "
			} else {
				next += "│  "
			}
		}
		for i, k := range v.Children {
			walk(k, next, i == len(v.Children)-1, depth+1)
		}
	}
	for _, v := range roots {
		walk(v, "", true, 0)
	}

	// cellText renders one column of one row into its styled string.
	cellText := func(r row, c Column) string {
		switch c {
		case ColNode:
			return tui.Accent(sanitizeCell(r.label))
		case ColKind:
			return tui.Muted(string(r.v.Kind))
		case ColVol:
			return styleCell(r.v.Volume)
		case ColHeld:
			return styleCell(r.v.Held)
		case ColTmp:
			return styleCell(r.v.Tmp)
		case ColState:
			return styleState(r.v.State)
		case ColWhere:
			return tui.Muted(sanitizeCell(r.v.Where))
		default:
			return ""
		}
	}

	// One width per column across the whole forest, measured by visible width so
	// ANSI-styled cells still line up.
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(columnTitle(c))
	}
	for _, r := range rows {
		for i, c := range cols {
			if w := lipgloss.Width(cellText(r, c)); w > widths[i] {
				widths[i] = w
			}
		}
	}

	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	writeLine := func(cells []string) {
		b.WriteString(pad)
		for i, cell := range cells {
			b.WriteString(cell)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)+2))
			}
		}
		b.WriteByte('\n')
	}

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = tui.Muted(columnTitle(c))
	}
	writeLine(header)
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = cellText(r, c)
		}
		writeLine(cells)
	}
	return b.String()
}

// styleCell colors a space cell: a space that holds files shows the amber ●;
// an empty space shows nothing; n/a recedes (muted).
func styleCell(c Cell) string {
	switch c {
	case CellHolds:
		return tui.Holds()
	default:
		return tui.Muted(c.Symbol())
	}
}

// styleState colors a box or worktree state cell: what draws the eye (a live
// box, unmerged work) is accented; what recedes (gone, merged) is muted. State
// is a box/worktree concept, so a node kind without one (project, workdir)
// leaves the cell blank rather than filling it with a placeholder glyph.
func styleState(c Cell) string {
	switch c {
	case CellNA:
		return ""
	case CellLive, CellUnmerged, CellHasWork:
		return tui.Accent(c.Symbol())
	default:
		return tui.Muted(c.Symbol())
	}
}
