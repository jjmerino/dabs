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

	"github.com/jjmerino/dabs/core/tui"
)

// wtLogEntry is one line of ~/.dabs/worktrees/log.jsonl, the append-only journal
// of worktree-backed box lifecycles. The log is the ONLY record of which
// instance belongs to which worktree, so `down` and the liveness column both
// read it back. Path/Recipe are carried on `up` entries only.
type wtLogEntry struct {
	Event    string `json:"event"` // "up" | "down"
	TS       string `json:"ts"`
	Instance string `json:"instance"`
	Worktree string `json:"worktree"`         // short name under ~/.dabs/worktrees
	Path     string `json:"path,omitempty"`   // absolute worktree path (up only)
	Recipe   string `json:"recipe,omitempty"` // recipe that made the box (up only)
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
	return filepath.Join(home, ".dabs", "worktrees", wtLogFile), nil
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

// liveByWorktree inverts liveBoxes to worktree name → live instance, for the
// per-worktree DETAIL column. If a worktree somehow has more than one live box,
// the last one in the log wins.
func (r Real) liveByWorktree() map[string]string {
	entries, err := r.readWtLog()
	if err != nil {
		return nil
	}
	byWt := map[string]string{}
	for inst, wt := range liveBoxes(entries) {
		byWt[wt] = inst
	}
	return byWt
}
