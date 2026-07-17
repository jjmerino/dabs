package actions

import (
	"fmt"
	"os"
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Cd prints a node's directory — its own dir under ~/.dabs/nodes/<id>, the
// same WHERE `dabs ls` shows, ONE uniform rule for every kind — as a bare
// absolute path. A child process cannot move its parent shell, so the verb is
// the printable half of the journey: cd "$(dabs cd <node>)". The three spaces
// are literal subdirectories of that path (volume/, held/, tmp/). The node
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
	dir, err := r.resolveNodeDir(hits[0].ID)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, dir)
	return nil
}
