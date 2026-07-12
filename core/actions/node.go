package actions

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// A NODE is one thing dabs provisioned and owns. It lives at
//
//	~/.dabs/nodes/<id>/
//	    dabs-node.json   — ours: what this node is, and the recipe that made it
//	    data/            — the user's: what the recipe's `.` resolves to
//
// Today the only kind is a worktree. The layout is deliberately kind-agnostic:
// a node's identity, provenance and lifetime are the same questions whatever
// was provisioned, so growing a new kind means adding a nest below, not a new
// directory tree.
const nodeFile = "dabs-node.json"

// Node is the record at ~/.dabs/nodes/<id>/dabs-node.json. Fields common to
// every node sit at the top; kind-specific fields nest under their own key, and
// the PRESENCE of that key is the kind — a node with a "worktree" nest is a
// worktree. Listing, reaping and casting all read this record rather than
// sniffing the filesystem, so dabs only ever sees what it actually made.
type Node struct {
	ID       string        `json:"id"`
	Recipe   string        `json:"recipe"`             // the recipe that provisioned it — provenance
	Created  string        `json:"created"`            // RFC3339
	Worktree *NodeWorktree `json:"worktree,omitempty"` // set ⇒ this node is a worktree
}

// NodeWorktree carries what every worktree operation needs: the branch (to
// delete on reap) and the parent repo (to remove the worktree from, and whose
// .git `cast` must mount so git resolves inside a box).
type NodeWorktree struct {
	Branch string `json:"branch"`
	Repo   string `json:"repo"`
}

// mintNodeID makes a node id — the ONE place ids are minted, so every node's
// name has the same shape: a readable prefix (what it came from) plus a short
// random suffix. It also returns that suffix, which callers reuse when they need
// a matching name elsewhere (a worktree's branch is `dabs/<short>`), so a node
// and the things it owns always share one id.
//
// Readable and prefix-matchable is the point: `dabs worktrees`, `cast <name>`
// and `down <name>` all resolve on a unique prefix, git-style.
func mintNodeID(prefix string) (id, short string) {
	short = randHex(4)
	return prefix + "-" + short, short
}

// resolveNodesRoot returns the directory every node lives under.
func (r Real) resolveNodesRoot() (string, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dabs", "nodes"), nil
}

// resolveNodeDir returns a node's own directory — ours.
func (r Real) resolveNodeDir(id string) (string, error) {
	root, err := r.resolveNodesRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

// resolveNodeData returns a node's data directory — the user's, and what a
// recipe's `.` resolves to.
func (r Real) resolveNodeData(id string) (string, error) {
	dir, err := r.resolveNodeDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "data"), nil
}

// writeNode persists a node's record.
func (r Real) writeNode(n Node) error {
	dir, err := r.resolveNodeDir(n.ID)
	if err != nil {
		return err
	}
	if err := r.data.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("node %s: %w", n.ID, err)
	}
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return fmt.Errorf("node %s: %w", n.ID, err)
	}
	return r.data.WriteFile(filepath.Join(dir, nodeFile), append(b, '\n'), 0o644)
}

// readNode loads one node's record by id.
func (r Real) readNode(id string) (Node, error) {
	dir, err := r.resolveNodeDir(id)
	if err != nil {
		return Node{}, err
	}
	b, err := r.data.ReadFile(filepath.Join(dir, nodeFile))
	if err != nil {
		return Node{}, fmt.Errorf("node %s: %w", id, err)
	}
	var n Node
	if err := json.Unmarshal(b, &n); err != nil {
		return Node{}, fmt.Errorf("node %s: %w", id, err)
	}
	return n, nil
}

// listNodes returns every node dabs owns. An entry without a readable record is
// not a node — dabs wrote it or it isn't ours — so stray files under nodes/ are
// ignored rather than guessed at.
func (r Real) listNodes() ([]Node, error) {
	root, err := r.resolveNodesRoot()
	if err != nil {
		return nil, err
	}
	names, err := r.data.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(names))
	for _, name := range names {
		n, err := r.readNode(name)
		if err != nil {
			continue // not a node dabs wrote
		}
		out = append(out, n)
	}
	return out, nil
}

// listWorktreeNodes returns only the nodes that are worktrees.
func (r Real) listWorktreeNodes() ([]Node, error) {
	all, err := r.listNodes()
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(all))
	for _, n := range all {
		if n.Worktree != nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// stampNow returns the timestamp node records are written with.
func stampNow() string { return time.Now().UTC().Format(time.RFC3339) }
