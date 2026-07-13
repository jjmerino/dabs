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
// The layout is kind-agnostic: a node's identity, provenance and lifetime are the
// same questions whatever was provisioned, so a new kind adds a nest below, not a
// new directory tree.
const nodeFile = "dabs-node.json"

// Node is the record at ~/.dabs/nodes/<id>/dabs-node.json. Fields common to
// every node sit at the top; kind-specific fields nest under their own key, and
// the PRESENCE of that key is the kind — a node with a "worktree" nest is a
// worktree. Listing, reaping and worktree-binding all read this record rather than
// sniffing the filesystem, so dabs only ever sees what it actually made.
type Node struct {
	ID      string   `json:"id"`
	Kind    NodeKind `json:"kind"`
	Parent  string   `json:"parent,omitempty"` // the node below this one in the chain
	Recipe  string   `json:"recipe"`           // the recipe that provisioned it — provenance
	Created string   `json:"created"`          // RFC3339
	// Dir is the place this node marks. For a project it is the cwd the command
	// ran from; for a workdir the host directory `.` resolved to. Empty for a box
	// (a box marks a sandbox, not a directory) and for a worktree (dabs made its
	// checkout, which lives in the node's own ephemeral space).
	Dir string `json:"dir,omitempty"`
	// Instance is the driver's name for a box node's sandbox. A node id is minted
	// before the box is up (its spaces must exist to be mounted), so the two names
	// are distinct and the node records the link.
	Instance string        `json:"instance,omitempty"`
	Worktree *NodeWorktree `json:"worktree,omitempty"` // kind-specific fields
}

// NodeKind is what a node marks. The chain a recipe builds is constrained to
//
//	project → (workdir | worktree)? → box
//
// and nothing else: a box never parents a directory, a worktree is never cut
// inside a box, boxes do not nest. The topology is fixed so that reading a chain
// never requires asking what an arbitrary nesting would mean.
type NodeKind string

const (
	// KindProject marks the directory a dabs command ran from. Every chain starts
	// with one, and dabs never reaps its Dir — that directory is the user's.
	KindProject NodeKind = "project"
	// KindWorkdir marks a host directory a recipe mounted or copied as `.`.
	KindWorkdir NodeKind = "workdir"
	// KindWorktree marks a git worktree dabs cut. Its checkout lives in the
	// node's ephemeral space, so dabs owns it and may reap it.
	KindWorktree NodeKind = "worktree"
	// KindBox marks a running sandbox, one per driver instance.
	KindBox NodeKind = "box"
)

// NodeWorktree carries what every worktree operation needs: the branch (to
// delete on reap) and the parent repo (to remove the worktree from, and whose
// .git `--worktree` must mount so git resolves inside a box).
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
// Readable and prefix-matchable is the point: `dabs worktrees`, `recipe --worktree <name>`
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

// A node offers three directories, and which one a recipe mounts declares what
// it expects to happen to the bytes. Convention, not configuration: `down` reads
// the space, not the recipe.
const (
	// SpaceVolume survives `down` — on a PLACE, which every later box re-enters.
	// Sessions, caches, anything wanted next time. A box's own volume is reaped
	// with it: a box node is never re-entered, so nothing in it could be found
	// again.
	SpaceVolume = "volume"
	// SpaceEphemeral is dabs's to reap, but not silently: `down` asks before
	// removing a non-empty one. A worktree's checkout lives here.
	SpaceEphemeral = "ephemeral"
	// SpaceTmp is scratch. `down` removes it without asking.
	SpaceTmp = "tmp"
)

// resolveNodeSpace returns one of a node's three directories.
func (r Real) resolveNodeSpace(id, space string) (string, error) {
	dir, err := r.resolveNodeDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, space), nil
}

// resolveNodeData returns the directory a node's `.` resolves to: a worktree's
// checkout, which dabs cut into the node's ephemeral space.
//
// Nodes written before the space layout keep their checkout in `data/`. Both are
// read, so a worktree made by an older dabs still lists, diffs and binds.
func (r Real) resolveNodeData(id string) (string, error) {
	eph, err := r.resolveNodeSpace(id, SpaceEphemeral)
	if err != nil {
		return "", err
	}
	wt := filepath.Join(eph, "worktree")
	if _, err := r.data.Stat(wt); err == nil {
		return wt, nil
	}
	dir, err := r.resolveNodeDir(id)
	if err != nil {
		return "", err
	}
	if legacy := filepath.Join(dir, "data"); r.dataExists(legacy) {
		return legacy, nil
	}
	return wt, nil
}

// dataExists reports whether a path is present, treating any error as absent.
func (r Real) dataExists(path string) bool {
	_, err := r.data.Stat(path)
	return err == nil
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
	// A record without an id is not a node dabs wrote: an empty or partial JSON
	// object ({}) unmarshals cleanly but carries no identity. Rejecting it keeps
	// such stray records out of the fleet — and out of the parent map, where an
	// empty id would otherwise point a chain walk at itself and never terminate.
	if n.ID == "" {
		return Node{}, fmt.Errorf("node %s: record has no id", id)
	}
	// A record with no kind carries a worktree nest, and the nest is what it is.
	if n.Kind == "" && n.Worktree != nil {
		n.Kind = KindWorktree
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
