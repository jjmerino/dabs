package actions

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/tui"
)

// Info renders ONE node's full model: its kind and id, the working place it
// marks, its three spaces (volume/held/tmp) with each one's presence, and the
// recipe that provisioned it. The node resolves like every other handle (exact
// id, id prefix, then a box instance name), and ambiguity is refused.
//
// The recipe comes from the SNAPSHOT captured on the node at creation
// (RecipeSpec) — the exact spec that provisioned it, truthful even when the
// registry has drifted since. A node written before snapshots existed has none,
// so info falls back to the current registry definition of its Recipe name,
// saying plainly that is what it shows.
func (r Real) Info(p params.Info) error {
	if strings.TrimSpace(p.Node) == "" {
		return fmt.Errorf("info: a node is required (see dabs ls)")
	}
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	hits := matchNodes(p.Node, nodes)
	if len(hits) == 0 {
		return fmt.Errorf("info: no node %q (see dabs ls)", p.Node)
	}
	if len(hits) > 1 {
		var ids []string
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		return fmt.Errorf("info: %q matches %d nodes (%s) — name one", p.Node, len(hits), strings.Join(ids, ", "))
	}
	n := hits[0]

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", tui.Accent(n.ID), tui.Muted("(%s)", string(n.Kind)))

	loc := r.workingDir(n)
	if loc == "" {
		if d, err := r.resolveNodeDir(n.ID); err == nil {
			loc = d
		}
	}
	rows := [][]string{
		{tui.Muted("location"), tilde(loc)},
	}
	if n.Instance != "" {
		rows = append(rows, []string{tui.Muted("instance"), n.Instance})
	}
	// The command appended to the recipe at boot — what this box was asked to do,
	// shown whole (the recipe snapshot carries the recipe's own command, not this).
	if len(n.Extra) > 0 {
		rows = append(rows, []string{tui.Muted("appended"), shellJoin(n.Extra)})
	}
	b.WriteString(tui.Indent(tui.Rows(nil, rows), 2) + "\n")

	// The three spaces, each with its presence — the SAME predicate `ls` and the
	// rm consent use, so "holds files" means exactly what it does elsewhere.
	spaceRows := [][]string{
		{tui.Muted("VOL"), spaceStatus(r.spaceCell(n.ID, SpaceVolume))},
		{tui.Muted("HELD"), spaceStatus(r.heldCell(n.ID))},
		{tui.Muted("TMP"), spaceStatus(r.spaceCell(n.ID, SpaceTmp))},
	}
	b.WriteString("\n" + tui.Heading("spaces:") + "\n")
	b.WriteString(tui.Indent(tui.Rows(nil, spaceRows), 2) + "\n")

	b.WriteString("\n" + tui.Heading("recipe:") + "\n")
	b.WriteString(tui.Indent(r.renderRecipe(n), 2) + "\n")

	fmt.Fprint(os.Stdout, b.String())
	return nil
}

// spaceStatus is a space cell's human word for `dabs info`: a space that holds
// files says so, an empty one says empty.
func spaceStatus(c Cell) string {
	if c == CellHolds {
		return "holds files"
	}
	return "empty"
}

// renderRecipe describes the recipe a node came from. It prefers the persisted
// snapshot (the truthful spec at provision time); with none it falls back to the
// current registry definition of the Recipe name, and says so. A node with no
// recipe name, or a name the registry no longer knows, says that plainly rather
// than erroring.
func (r Real) renderRecipe(n Node) string {
	if n.RecipeSpec != nil {
		return describeRecipe(n.Recipe, *n.RecipeSpec, "snapshot at creation")
	}
	if n.Recipe == "" {
		return tui.Muted("(none — this node was not made by a recipe)")
	}
	reg, err := r.loadRegistry()
	if err == nil {
		if rec, gerr := reg.Get(n.Recipe); gerr == nil {
			return describeRecipe(n.Recipe, rec, "current registry definition — no snapshot on this node")
		}
	}
	return tui.Muted("%s (no snapshot on this node, and the registry no longer defines it)", n.Recipe)
}

// describeRecipe renders a recipe's name, image, command, env, and sources. The
// note names where the spec came from — a snapshot, or the live registry.
func describeRecipe(name string, rec recipe.Recipe, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", tui.Accent(name), tui.Muted("(%s)", note))

	rows := [][]string{
		{tui.Muted("image"), describeImage(rec.Image)},
	}
	if len(rec.Command) > 0 {
		rows = append(rows, []string{tui.Muted("command"), strings.Join(rec.Command, " ")})
	}
	if rec.Target != "" {
		rows = append(rows, []string{tui.Muted("target"), rec.Target})
	}
	b.WriteString(tui.Rows(nil, rows))

	if len(rec.Env) > 0 {
		keys := make([]string, 0, len(rec.Env))
		for k := range rec.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n" + tui.Muted("env:") + "\n")
		var envRows [][]string
		for _, k := range keys {
			envRows = append(envRows, []string{"  " + k + "=" + rec.Env[k]})
		}
		b.WriteString(tui.Rows(nil, envRows))
	}

	if len(rec.Sources) > 0 {
		b.WriteString("\n" + tui.Muted("mounts:") + "\n")
		var srcRows [][]string
		for _, s := range rec.Sources {
			kind, origin, err := s.Kind()
			if err != nil {
				kind, origin = "?", ""
			}
			// A boxless source (a worktree/copy that only provisions a place) has no
			// in-box target, so there is nothing to point an arrow at.
			if s.Path == "" {
				srcRows = append(srcRows, []string{"  " + kind, origin})
				continue
			}
			srcRows = append(srcRows, []string{"  " + kind, origin, tui.Arrow(), s.Path})
		}
		b.WriteString(tui.Rows(nil, srcRows))
	}
	return b.String()
}

// describeImage names a recipe's image: a reused image by name, or a Dockerfile
// build with its context.
func describeImage(img recipe.ImageRef) string {
	if img.Name != "" {
		return img.Name
	}
	if img.Dockerfile != "" {
		ctx := img.Context
		if ctx == "" {
			ctx = "."
		}
		return fmt.Sprintf("build %s (context %s)", img.Dockerfile, ctx)
	}
	return "(none)"
}
