package actions_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// logLine is the shape of one journal entry, for parsing back what the actions
// wrote through the data seam.
type logLine struct {
	Event    string `json:"event"`
	Instance string `json:"instance"`
	Worktree string `json:"worktree"`
	Path     string `json:"path"`
	Recipe   string `json:"recipe"`
}

// readLog parses every JSON line the fake recorded at ~/.dabs/worktrees/log.jsonl.
func readLog(t *testing.T, fd *fakeData) []logLine {
	t.Helper()
	b := fd.files[wtBase+"/log.jsonl"]
	var out []logLine
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e logLine
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// CONTRACT: bringing up a worktree-backed box (a `worktree:` recipe) appends one
// `up` entry linking the box instance to the fresh worktree's name and abs path.
func TestRecipeWorktreeLogsUp(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData()
	fd.toplevel["."] = nil
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "w"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	log := readLog(t, fd)
	if len(log) != 1 || log[0].Event != "up" {
		t.Fatalf("want one up entry, got %+v", log)
	}
	e := log[0]
	if e.Instance != "img-inst" || e.Recipe != "w" {
		t.Fatalf("up entry instance/recipe wrong: %+v", e)
	}
	// The worktree the box was cut is the one the fake recorded; path is absolute.
	if e.Path != fd.worktrees[0] || e.Worktree == "" || !strings.HasPrefix(e.Path, wtBase) {
		t.Fatalf("up entry worktree/path wrong: %+v (created %v)", e, fd.worktrees)
	}
}

// CONTRACT: a PLAIN (non-worktree) box writes nothing to the journal.
func TestRecipeMountLogsNothing(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.exists["/data"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(readLog(t, fd)) != 0 {
		t.Fatalf("plain box should log nothing, got %v", fd.files[wtBase+"/log.jsonl"])
	}
}

// CONTRACT: `dabs down` on a worktree-backed box looks the worktree up FROM the
// log (its only record) and appends a matching `down`. A repeated down adds
// nothing (the instance is no longer live).
func TestDownLogsWorktreeDownFromLog(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{
		wtBase + "/log.jsonl": []byte(
			`{"event":"up","ts":"t1","instance":"img-inst","worktree":"proj-aa","path":"` + wtBase + `/proj-aa","recipe":"w"}` + "\n"),
	}
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "img-inst", Driver: "fake"}}}
	r := newReal("", fd, drv)
	if err := r.Down(params.Down{Instance: "img-inst"}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	log := readLog(t, fd)
	if len(log) != 2 || log[1].Event != "down" || log[1].Worktree != "proj-aa" || log[1].Instance != "img-inst" {
		t.Fatalf("want a down entry resolved to proj-aa, got %+v", log)
	}
	// A second down finds no live box → no new entry.
	if err := r.Down(params.Down{Instance: "img-inst"}); err != nil {
		t.Fatalf("Down again: %v", err)
	}
	if got := readLog(t, fd); len(got) != 2 {
		t.Fatalf("repeated down should add nothing, got %+v", got)
	}
}

// CONTRACT: downing a PLAIN box (not in the journal) writes no `down` entry.
func TestDownPlainBoxLogsNothing(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "plain-inst", Driver: "fake"}}}
	if err := newReal("", fd, drv).Down(params.Down{Instance: "plain-inst"}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(readLog(t, fd)) != 0 {
		t.Fatalf("plain box down should log nothing, got %v", fd.files[wtBase+"/log.jsonl"])
	}
}
