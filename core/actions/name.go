package actions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
func validateNodeName(name string) error { return validateIDShape("--name", name) }

// validateIDShape is that check over any string destined to BE a node id or to
// prefix one. The what argument names the source of the string — a flag, a
// field — so the message points at whoever supplied it.
func validateIDShape(what, name string) error {
	if !nodeNameRe.MatchString(name) {
		return fmt.Errorf("%s %q: a name is letters, digits, dots, underscores, dashes — starting alphanumeric, at most 64", what, name)
	}
	if strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("%s %q: `..`, a trailing dot, or a .lock suffix cannot name a git branch (dabs/%s)", what, name, name)
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
			return refuseHeldName("--name %q: box %s runs under that instance name — one handle must not mean two boxes", name, n.ID)
		}
	}
	if holder != nil {
		if err := r.reapInactiveHolder(name, nodes); err != nil {
			return err
		}
	}
	return r.reserveNodeDir(name)
}

// ErrNameHeld marks every refusal that means THE NAME IS TAKEN: a live holder,
// a holder that cannot be verified, an instance already running under it, or
// another boot mid-claim. A caller that names its boxes after something of its
// own — a session, a ticket — collides on one name and no other, so it can turn
// this one condition into its own instruction (whose box it is, how to clear
// it) instead of forwarding a message about dabs's node store. Every other boot
// failure stays what it is.
var ErrNameHeld = errors.New("the requested node name is held")

// nameHeld carries a refusal's own words AND the sentinel, so matching on the
// condition never costs the message that says which name and why.
type nameHeld struct{ reason error }

func (e nameHeld) Error() string   { return e.reason.Error() }
func (e nameHeld) Unwrap() []error { return []error{e.reason, ErrNameHeld} }

// refuseHeldName writes one such refusal.
func refuseHeldName(format string, a ...any) error {
	return nameHeld{reason: fmt.Errorf(format, a...)}
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
			return refuseHeldName("--name %q: a node holds that name and a driver did not answer — cannot verify it is inactive", name)
		}
	}
	if r.claimHolderActive(subtree, ans, r.foreignWorktrees(nodes)) {
		return refuseHeldName("--name %q: an active node holds that name (see dabs ls) — pick another, or reap it first", name)
	}
	fmt.Fprintln(os.Stdout, tui.Muted("name %s: held by an inactive node — reaping it", name))
	states := func() driversAnswer { return ans }
	if err := r.rmResolved(params.Rm{Node: name, Yes: true}, nodes, states); err != nil {
		return fmt.Errorf("--name %q: reap the inactive holder: %w", name, err)
	}
	return nil
}

// claimHolderActive answers the one question the claim asks of a holder: is
// there anything under this name a boot must not take? It is activeSubtrees'
// judgment narrowed to one subtree — a node is life for the name it stands
// under, and life propagates UP, so any live node in the holder's subtree
// holds the name and nothing outside it can — and it judges each node by
// claimSelfActive rather than nodeSelfActive. A project whose repo carries an
// unmerged externally-managed worktree is live here as it is everywhere: the
// checkout is work, whoever cut it.
func (r Real) claimHolderActive(subtree []Node, ans driversAnswer, foreign map[string][]*NodeView) bool {
	for _, n := range subtree {
		if len(foreign[n.ID]) > 0 {
			return true
		}
		if r.claimSelfActive(n, ans.state, ans.complete) {
			return true
		}
	}
	return false
}

// claimSelfActive is one node's claim to life AS A CLAIM READS IT: everything
// nodeSelfActive counts, minus tmp/.
//
// tmp is the space `rm` reaps quietly, with no consent asked, because the node
// declared it scratch. Bytes nobody has to be asked about before they are
// deleted cannot be the thing that makes a name unavailable forever — and a box
// gets tmp bytes for free: the relay dabs itself starts writes its socket lock
// and its log there. A box whose instance is gone from a COMPLETE drivers'
// answer would otherwise read as alive on nothing but that litter, and the name
// it holds — for a caller that names boxes after a thing of its own, the only
// name that will do — could never be claimed again.
//
// The narrowing is the CLAIM's alone. `ls` and `rm --inactive` keep asking
// nodeSelfActive, so what they show and sweep does not move: a record holding
// only scratch is still a live row there, and only a boot asking for its exact
// name reaps it.
func (r Real) claimSelfActive(n Node, state map[string]boxState, complete bool) bool {
	return r.nodeHoldsLife(n, state, complete, r.nodeKeptSpaceDirs(n))
}

// claimMarker is the receipt a reservation leaves in the node dir until the
// node record lands: its presence and age are what tell a CONCURRENT claim (in
// the window between another boot's reserve and its record) apart from the
// litter of a boot that died there. Without it, reclaiming would reopen the
// very race the reservation closes — deleting a live claim and letting two
// boots share one name.
const claimMarker = "dabs-claim"

// claimStale is how old a claim marker must be before its dir counts as
// litter. The claim-to-record window spans provisioning, possibly a docker
// image build, and the box boot — usually seconds, but a cold build can run
// long, and a build that outlives this bound leaves its claim reclaimable by a
// concurrent same-name boot. The bound trades that narrow self-race against
// dead names squatting for hours; `dabs rm <name>` clears a dead claim at any
// age.
const claimStale = time.Hour

// reserveNodeDir is the exclusive create that makes a claim exclusive — the
// same mint lock ensureProjectNode uses. On finding the dir taken: a node
// record means a winner (refuse); a FRESH claim marker means a boot mid-claim
// (refuse — retry shortly); a stale or absent marker is litter, reclaimed once.
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
			return r.data.WriteFile(filepath.Join(dir, claimMarker), []byte(stampNow()+"\n"), 0o644)
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if _, rerr := r.readNode(name); rerr == nil {
			return refuseHeldName("--name %q: another boot just claimed it", name)
		}
		if fresh, err := r.claimIsFresh(dir); err != nil {
			return err
		} else if fresh {
			return refuseHeldName("--name %q: another boot is claiming it right now (or died within %s) — retry shortly, or `dabs rm %s -y`", name, claimStale, name)
		}
		// No record and no fresh marker: litter — but a concurrent boot sits in
		// the gap between ITS Mkdir and its marker write for a moment, so look
		// again after a beat before deleting anything from under anyone.
		time.Sleep(150 * time.Millisecond)
		if _, rerr := r.readNode(name); rerr == nil {
			return refuseHeldName("--name %q: another boot just claimed it", name)
		}
		if fresh, err := r.claimIsFresh(dir); err != nil {
			return err
		} else if fresh {
			return refuseHeldName("--name %q: another boot is claiming it right now (or died within %s) — retry shortly, or `dabs rm %s -y`", name, claimStale, name)
		}
		if err := r.data.RemoveAll(dir); err != nil {
			return err
		}
	}
	return refuseHeldName("--name %q: could not reserve the node dir", name)
}

// claimIsFresh reads a reservation's marker: present and younger than
// claimStale means a boot may be mid-claim. An unparseable marker reads as
// FRESH — garbage is not proof the claim is dead, and `dabs rm <name> -y`
// clears it either way.
func (r Real) claimIsFresh(dir string) (bool, error) {
	b, err := r.data.ReadFile(filepath.Join(dir, claimMarker))
	if err != nil {
		return false, nil // no marker: pre-marker litter
	}
	at, perr := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if perr != nil {
		return true, nil
	}
	return time.Since(at) < claimStale, nil
}
