package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Cd prints the directory a node marks — the same WHERE `dabs ls` shows — as a
// bare absolute path. A child process cannot move its parent shell, so the verb
// is the printable half of the journey: cd "$(dabs cd <node>)". The node
// resolves like every other handle (exact id, id prefix, then a box instance
// name), and ambiguity is refused: a cd that guesses lands somewhere wrong.
func (r Real) Cd(p params.Cd) error {
	if strings.TrimSpace(p.Node) == "" {
		return fmt.Errorf("cd: a node is required (see dabs ls)")
	}
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	hits := matchNodes(p.Node, nodes)
	if len(hits) == 0 {
		return fmt.Errorf("cd: no node %q (see dabs ls)", p.Node)
	}
	if len(hits) > 1 {
		var ids []string
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		return fmt.Errorf("cd: %q matches %d nodes (%s) — name one", p.Node, len(hits), strings.Join(ids, ", "))
	}
	n := hits[0]
	dir := ""
	switch n.Kind {
	case KindBox:
		// A box marks a sandbox; its bytes live in its node dir (the spaces).
		dir, err = r.resolveNodeDir(n.ID)
		if err != nil {
			return err
		}
	case KindWorktree, KindWorkdir:
		dir = r.nodeFolder(n)
	default: // project: the directory the command ran from — the user's own
		dir = n.Dir
	}
	if dir == "" {
		return fmt.Errorf("cd: node %s marks no directory", n.ID)
	}
	fmt.Fprintln(os.Stdout, dir)
	return nil
}
