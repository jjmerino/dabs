package actions

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jjmerino/dabs/core/recipe"
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
	ID     string   `json:"id"`
	Kind   NodeKind `json:"kind"`
	Parent string   `json:"parent,omitempty"` // the node below this one in the chain
	Recipe string   `json:"recipe"`           // the recipe that provisioned it — provenance
	// RecipeSpec is a SNAPSHOT of the fully-resolved recipe captured when this node
	// was provisioned: the image, command, env, and sources as they stood at
	// creation. `dabs info` renders THIS, not a fresh registry lookup of Recipe,
	// which may have drifted since. Nil on nodes written before snapshots existed,
	// and on nodes no recipe made; info falls back to the registry by Recipe name.
	RecipeSpec *recipe.Recipe `json:"recipeSpec,omitempty"`
	Created    string         `json:"created"` // RFC3339
	// Dir is the place this node marks. For a project it is the cwd the command
	// ran from; for a workdir the host directory `.` resolved to. Empty for a box
	// (a box marks a sandbox, not a directory) and for a worktree dabs cut (its
	// checkout lives in the node's own held space). A worktree node WITHOUT a
	// Worktree nest is an externally-managed checkout dabs ran from — Dir is
	// that checkout's path, and dabs never reaps its bytes.
	Dir string `json:"dir,omitempty"`
	// Instance is the driver's name for a box node's sandbox. A node id is minted
	// before the box is up (its spaces must exist to be mounted), so the two names
	// are distinct and the node records the link.
	Instance string `json:"instance,omitempty"`
	// Extra is the argv appended to the recipe's command when this box was booted
	// (`dabs recipe <name> <extra…>`) — the provenance of what THIS box was asked
	// to do, which the recipe snapshot cannot carry. Empty when nothing was
	// appended, and set only on box nodes.
	Extra    []string      `json:"extra,omitempty"`
	Worktree *NodeWorktree `json:"worktree,omitempty"` // kind-specific fields
	// ProxyPID and ProxyDir track a box's proxy engine — a host-side sidecar
	// started when the recipe has a `proxies:` chain. They let any later dabs
	// process (a `dabs rm` that never held the Engine object) reap the engine
	// and its temp files when the box comes down. Zero when the box has no proxy.
	ProxyPID int    `json:"proxyPid,omitempty"`
	ProxyDir string `json:"proxyDir,omitempty"`
}

// NodeKind is what a node marks. The chain a recipe builds is constrained to
//
//	project → (workdir | worktree)? → box
//
// and nothing else: a box never parents a directory, a worktree is never cut
// inside a box, boxes do not nest. A chain rooted in a LINKED git worktree
// starts at its externally-managed worktree marker — the project slot is
// simply absent when dabs does not track the repo's main checkout. The
// topology is fixed so that reading a chain never requires asking what an
// arbitrary nesting would mean.
type NodeKind string

const (
	// KindProject marks a plain directory (or a repo's main checkout) a dabs
	// command ran from. dabs never reaps its Dir — that directory is the user's.
	KindProject NodeKind = "project"
	// KindWorkdir marks a host directory a recipe mounted or copied as `.`.
	KindWorkdir NodeKind = "workdir"
	// KindWorktree marks a git worktree: one dabs cut (its Worktree nest is
	// set; the checkout lives in the node's held space, so dabs owns it and
	// may reap it), or an externally-managed checkout dabs ran from (no
	// Worktree nest; Dir is the checkout, never reaped — see
	// ensureProjectNode).
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

// appendedCommand returns the FULL effective command a box was booted with — the
// recipe's base command (from the creation snapshot) followed by the tokens the
// caller appended (Extra) — shell-joined for display. A node with no snapshot
// (an old record) falls back to the appended tokens alone: they are all it has.
// Empty when nothing was appended, so a caller can gate on that to decide whether
// to show the cell at all.
func (n Node) appendedCommand() string {
	if len(n.Extra) == 0 {
		return ""
	}
	var argv []string
	if n.RecipeSpec != nil {
		argv = append(argv, n.RecipeSpec.Command...)
	}
	argv = append(argv, n.Extra...)
	return shellJoin(argv)
}

// mintNodeID makes a node id — the ONE place ids are minted, so every node's
// name has the same shape: a readable prefix (what it came from) plus a short
// random suffix. It also returns that suffix, which callers reuse when they need
// a matching name elsewhere (a worktree's branch is `dabs/<short>`), so a node
// and the things it owns always share one id.
//
// Readable and prefix-matchable is the point: `dabs worktrees`, `recipe --worktree <name>`
// and `rm <name>` all resolve on a unique prefix, git-style.
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
// it expects to happen to the bytes. Convention, not configuration: `rm` reads
// the space, not the recipe.
const (
	// SpaceVolume survives an `rm` unless --volume names it — on a PLACE, which
	// every later box re-enters. Sessions, caches, anything wanted next time. A
	// box's own volume follows the same rule (it is kept without --volume), but a
	// box is never re-entered, so nothing put there is found again.
	SpaceVolume = "volume"
	// SpaceHeld is dabs's to reap, but not silently: something outside the box
	// points at it (a worktree's checkout lives here, review tooling reads it), so
	// `rm` asks before removing a non-empty one — deleting it breaks someone else.
	SpaceHeld = "held"
	// SpaceHeldLegacy is the on-disk name held spaces had before the rename. New
	// nodes create held/; a node written by an older dabs keeps its ephemeral/
	// dir, and resolveNodeData falls back to it so it still lists, diffs and binds.
	SpaceHeldLegacy = "ephemeral"
	// SpaceTmp is scratch. `rm` removes it without asking and never reads it to
	// decide anything.
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
// checkout, which dabs cut into the node's held space.
//
// Two legacy layouts are still read, so a worktree made by an older dabs keeps
// working: a node whose held space is named ephemeral/ (the space's former name),
// and a node written before the space layout that keeps its checkout in `data/`.
// A new node's checkout is held/worktree; that is what is returned when nothing
// legacy is present.
func (r Real) resolveNodeData(id string) (string, error) {
	held, err := r.resolveNodeSpace(id, SpaceHeld)
	if err != nil {
		return "", err
	}
	wt := filepath.Join(held, "worktree")
	if _, err := r.data.Stat(wt); err == nil {
		return wt, nil
	}
	if legacy, err := r.resolveNodeSpace(id, SpaceHeldLegacy); err == nil {
		if lwt := filepath.Join(legacy, "worktree"); r.dataExists(lwt) {
			return lwt, nil
		}
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

// resolveHeldSpace returns a node's held space directory, preferring the current
// held/ name but falling back to a legacy ephemeral/ dir an older dabs wrote —
// so the held-space guards (the ls ●, the rm consent) still see a legacy node's
// files. When neither is present it returns held/, the name a new node uses.
func (r Real) resolveHeldSpace(id string) (string, error) {
	held, err := r.resolveNodeSpace(id, SpaceHeld)
	if err != nil {
		return "", err
	}
	if r.dataExists(held) {
		return held, nil
	}
	legacy, err := r.resolveNodeSpace(id, SpaceHeldLegacy)
	if err != nil {
		return "", err
	}
	if r.dataExists(legacy) {
		return legacy, nil
	}
	return held, nil
}

// dataExists reports whether a path is present, treating any error as absent.
func (r Real) dataExists(path string) bool {
	_, err := r.data.Stat(path)
	return err == nil
}

// boxProxy returns the proxy engine PID/dir recorded on the box node named by
// instance, or 0,"" if the box has none — the pair proxy.Reap needs to stop the
// engine when the box comes down.
func (r Real) boxProxy(instance string) (int, string) {
	nodes, err := r.listNodes()
	if err != nil {
		return 0, ""
	}
	for _, n := range nodes {
		if n.Kind == KindBox && n.Instance == instance {
			return n.ProxyPID, n.ProxyDir
		}
	}
	return 0, ""
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
