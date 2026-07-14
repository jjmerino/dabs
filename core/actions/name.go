package actions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// A chosen node name becomes the node's ID — its one handle, shown wherever
// ids are shown and resolvable wherever ids resolve. The shape is the minted
// ids' own (letters, digits, dot, underscore, dash), so a name never needs
// quoting and never splits a row.
var nodeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validateNodeName rejects a name whose shape cannot serve everywhere the id
// goes: node dirs, rendered rows, and — for a worktree — the branch
// dabs/<name>, which git refuses for `..`, a trailing dot, or a .lock suffix.
// Checked up front, so a bad name costs nothing instead of failing at git
// after provisioning began.
func validateNodeName(name string) error {
	if !nodeNameRe.MatchString(name) {
		return fmt.Errorf("--name %q: a name is letters, digits, dots, underscores, dashes — starting alphanumeric, at most 64", name)
	}
	if strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("--name %q: `..`, a trailing dot, or a .lock suffix cannot name a git branch (dabs/%s)", name, name)
	}
	return nil
}

// claimNodeName frees a requested node name and RESERVES it, before anything
// is minted. Names are unique across every known node — ids and the instance
// names their boxes run under alike, so one handle can never mean two boxes. A
// holder whose subtree is INACTIVE — no live box, no real bytes, the empty
// markers `ls` hides — is a record, not data, so it is reaped on the spot
// rather than making every name single-use. An ACTIVE holder refuses the
// claim; so does one that cannot be verified (a box among its nodes and a
// driver that did not answer — silence is not proof the box is gone).
//
// The reservation is the mint lock ensureProjectNode uses: an exclusive create
// of the node dir. Two boots claiming one name race to that create; exactly
// one wins, the other refuses instead of silently overwriting the winner's
// record. A dir holding NO record is an earlier boot's litter (it died between
// claim and record) and is reclaimed.
func (r Real) claimNodeName(name string) error {
	if err := validateNodeName(name); err != nil {
		return err
	}
	nodes, err := r.listNodes()
	if err != nil {
		return err
	}
	var holder *Node
	for i, n := range nodes {
		if n.ID == name {
			holder = &nodes[i]
			continue
		}
		if n.Kind == KindBox && n.Instance == name {
			return fmt.Errorf("--name %q: box %s runs under that instance name — one handle must not mean two boxes", name, n.ID)
		}
	}
	if holder != nil {
		if err := r.reapInactiveHolder(name, nodes); err != nil {
			return err
		}
	}
	return r.reserveNodeDir(name)
}

// reapInactiveHolder clears the node holding a claimed name, when that is
// nothing but an empty record. Deciding "inactive" needs the drivers' answer
// only when a BOX is among the holder's nodes; a boxless holder's activity is
// its files alone, so a down server never blocks renaming over a local record.
func (r Real) reapInactiveHolder(name string, nodes []Node) error {
	subtree := append([]Node{}, descendantsOf(Node{ID: name}, nodes)...)
	for _, n := range nodes {
		if n.ID == name {
			subtree = append(subtree, n)
		}
	}
	hasBox := false
	for _, n := range subtree {
		if n.Kind == KindBox {
			hasBox = true
			break
		}
	}
	ans := driversAnswer{state: map[string]boxState{}, complete: true}
	if hasBox {
		ans = r.boxStates()
		if !ans.complete {
			return fmt.Errorf("--name %q: a node holds that name and a driver did not answer — cannot verify it is inactive", name)
		}
	}
	active, _ := r.activeSubtrees(nodes, ans.state)
	if active[name] {
		return fmt.Errorf("--name %q: an active node holds that name (see dabs ls) — pick another, or reap it first", name)
	}
	fmt.Fprintln(os.Stdout, tui.Muted("name %s: held by an inactive node — reaping it", name))
	states := func() driversAnswer { return ans }
	if err := r.rmResolved(params.Rm{Node: name, Yes: true}, nodes, states); err != nil {
		return fmt.Errorf("--name %q: reap the inactive holder: %w", name, err)
	}
	return nil
}

// reserveNodeDir is the exclusive create that makes a claim exclusive. A dir
// that exists but holds no node record is litter from a boot that died before
// writing one — reclaimed once; a dir with a record is a concurrent winner.
func (r Real) reserveNodeDir(name string) error {
	root, err := r.resolveNodesRoot()
	if err != nil {
		return err
	}
	if err := r.data.MkdirAll(root, 0o755); err != nil {
		return err
	}
	dir, err := r.resolveNodeDir(name)
	if err != nil {
		return err
	}
	for range [2]int{} {
		err := r.data.Mkdir(dir, 0o755)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if _, rerr := r.readNode(name); rerr == nil {
			return fmt.Errorf("--name %q: another boot just claimed it", name)
		}
		if err := r.data.RemoveAll(dir); err != nil {
			return err
		}
	}
	return fmt.Errorf("--name %q: could not reserve the node dir", name)
}
