package actions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// wtLogEntry is one line of ~/.dabs/worktrees/log.jsonl, the append-only journal
// of worktree-backed box lifecycles. The log is the ONLY record of which
// instance belongs to which worktree, so `down` and the liveness column both
// read it back.
//
// Only Event, Instance and Worktree are ever read back (for the down-lookup and
// the liveness fold). TS, Path and Recipe are INSPECTION-ONLY: they are written
// for a human (or a future tool) reading the journal and are never parsed by
// dabs itself. They ride on `up` entries only.
type wtLogEntry struct {
	Event    string `json:"event"` // "up" | "down" — read back
	Instance string `json:"instance"`
	Worktree string `json:"worktree"`         // short name under ~/.dabs/worktrees — read back
	TS       string `json:"ts"`               // inspection-only
	Path     string `json:"path,omitempty"`   // absolute worktree path (up only) — inspection-only
	Recipe   string `json:"recipe,omitempty"` // recipe that made the box (up only) — inspection-only
}

// wtLogFile is the journal's basename; it sits in ~/.dabs/worktrees alongside
// the worktree dirs, so the worktrees listing must skip it (see worktreeNames).
const wtLogFile = "log.jsonl"

// wtLogPath is ~/.dabs/worktrees/log.jsonl.
func (r Real) wtLogPath() (string, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dabs", wtLogFile), nil
}

// logWorktreeUp records that a worktree-backed box came up. Best-effort: a log
// failure warns but never breaks `dabs recipe`/`up`/`cast`.
func (r Real) logWorktreeUp(instance, worktree, path, recipe string) {
	r.appendWtLog(wtLogEntry{
		Event:    "up",
		TS:       time.Now().UTC().Format(time.RFC3339),
		Instance: instance,
		Worktree: worktree,
		Path:     path,
		Recipe:   recipe,
	})
}

// logWorktreeDown records that a worktree-backed box came down. The log is the
// source of truth for instance→worktree: it looks the worktree up from the
// journal and only writes when that instance is currently live (an `up` with no
// later `down`), so a plain box or a repeated `dabs down` adds nothing.
func (r Real) logWorktreeDown(instance string) {
	entries, err := r.readWtLog()
	if err != nil {
		return // nothing to reconcile against; best-effort
	}
	worktree, live := liveBoxes(entries)[instance]
	if !live {
		return // not a live worktree-backed box → no entry
	}
	r.appendWtLog(wtLogEntry{
		Event:    "down",
		TS:       time.Now().UTC().Format(time.RFC3339),
		Instance: instance,
		Worktree: worktree,
	})
}

// appendWtLog appends one JSON line to the journal. Resilient by design: any
// failure surfaces a warning on stderr and is swallowed — the box lifecycle
// must not hinge on the log.
func (r Real) appendWtLog(e wtLogEntry) {
	path, err := r.wtLogPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.Warn("worktree log: %v", err))
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.Warn("worktree log: %v", err))
		return
	}
	b = append(b, '\n')
	_ = r.data.MkdirAll(filepath.Dir(path), 0o755)
	if err := r.data.AppendFile(path, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, tui.Warn("worktree log: %v", err))
	}
}

// readWtLog parses the journal into entries in file order. A missing log is no
// entries, not an error. Unparseable lines are skipped so one bad line never
// blinds the whole log.
func (r Real) readWtLog() ([]wtLogEntry, error) {
	path, err := r.wtLogPath()
	if err != nil {
		return nil, err
	}
	b, err := r.data.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []wtLogEntry
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e wtLogEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// liveBoxes replays the journal to the set of instances currently up, returning
// instance → worktree name. An `up` marks an instance live; a later `down` for
// the same instance clears it. This is the log-derived liveness both `down` and
// the worktrees DETAIL column rely on.
//
// This fold IS the on-read compaction: every up/down pair collapses to nothing,
// so however long the file grows, only currently-live instances survive the
// computation — dead entries never accumulate in memory. (The file itself is
// append-only and is not rewritten; a size-bounded rewrite is left out of scope.)
func liveBoxes(entries []wtLogEntry) map[string]string {
	live := map[string]string{}
	for _, e := range entries {
		switch e.Event {
		case "up":
			live[e.Instance] = e.Worktree
		case "down":
			delete(live, e.Instance)
		}
	}
	return live
}

// fleetInstances is the set of instance names actually alive across the whole
// driver fleet, so the journal can be reconciled against reality. Best-effort:
// a driver that errors/times out is skipped (liveness is advisory, never worth
// hanging or failing `dabs worktrees`).
func (r Real) fleetInstances() map[string]bool {
	alive := map[string]bool{}
	for _, drv := range r.drivers {
		infos, err := lsTimeout(drv, remoteTimeout)
		if err != nil {
			continue
		}
		for _, in := range infos {
			alive[in.Name] = true
		}
	}
	return alive
}

// liveByWorktree inverts liveBoxes to worktree name → live instance, for the
// per-worktree DETAIL column, reconciled against the real fleet: a worktree is
// reported live only when the journal AND the drivers agree its box exists. A
// dangling `up` left by a crash, reboot, OOM, or a manual `docker rm` thus reads
// as "no box" rather than a phantom live. If a worktree somehow has more than one
// live box, the last one in the log wins.
func (r Real) liveByWorktree() map[string]string {
	entries, err := r.readWtLog()
	if err != nil {
		return nil
	}
	alive := r.fleetInstances()
	byWt := map[string]string{}
	for inst, wt := range liveBoxes(entries) {
		if alive[inst] {
			byWt[wt] = inst
		}
	}
	return byWt
}

// teardown brings a box down and, when it was a journaled worktree-backed box,
// records the matching `down` — keeping the journal balanced on EVERY teardown
// that follows a journaled `up`: the non-keep recipe teardown and buildBox's
// post-`up` failure cleanup. A plain (non-worktree) box logs nothing, because
// logWorktreeDown no-ops when the instance isn't a live journal entry.
func (r Real) teardown(drv sandbox.Driver, instance string) {
	drv.Down(instance)
	r.logWorktreeDown(instance)
}
