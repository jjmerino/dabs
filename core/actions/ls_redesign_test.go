package actions_test

// Tests for the ls redesign: one flat local tree across every local driver, a
// driver tag on each box, and a muted (location) row under any project or
// worktree whose real working place differs from its record dir.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// lineWith returns the first output line containing sub, for column assertions.
func lineWith(out, sub string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

// CONTRACT: every LOCAL driver collapses into ONE flat tree with no per-driver
// heading — a project with a box on the apple driver AND a box on the docker
// driver appears once, both boxes under it, and neither "local (" nor "docker ("
// heading is printed. Each box carries its driver tag in the KIND column.
func TestLsFlatLocalTreeAcrossDrivers(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj/dabs-node.json": []byte(`{"id":"proj","kind":"project","dir":"/repo","created":"1"}`),
		base + "boxa/dabs-node.json": []byte(`{"id":"boxa","kind":"box","parent":"proj","instance":"inst-apple","created":"2"}`),
		base + "boxd/dabs-node.json": []byte(`{"id":"boxd","kind":"box","parent":"proj","instance":"inst-docker","created":"3"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj", "boxa", "boxd"}}
	apple := &fakeDriver{kind: "apple", infos: []sandbox.Info{{Name: "inst-apple", Status: "running"}}}
	docker := &fakeDriver{kind: "docker", infos: []sandbox.Info{{Name: "inst-docker", Status: "running"}}}
	r := actions.New(
		map[string]sandbox.Driver{"local": apple, "docker": docker},
		[]string{"local", "docker"}, fstest.MapFS{}, fd,
	)

	out := captureStdout(t, func() {
		if err := r.Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	if strings.Contains(out, "this machine") || strings.Contains(out, "docker (docker)") {
		t.Fatalf("local drivers must not print per-driver headings:\n%s", out)
	}
	projRows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "project") {
			projRows++
		}
	}
	if projRows != 1 {
		t.Fatalf("the shared project must render exactly once:\n%s", out)
	}
	if l := lineWith(out, "boxa"); !strings.Contains(l, "box (apple)") {
		t.Fatalf("apple box must carry its driver tag:\n%s", out)
	}
	if l := lineWith(out, "boxd"); !strings.Contains(l, "box (docker)") {
		t.Fatalf("docker box must carry its driver tag:\n%s", out)
	}
}

// CONTRACT: a remote server KEEPS its own heading and section, while the local
// driver's boxes stay in the flat heading-less tree above it. A box under a
// server section carries no driver tag — the heading already names the server.
func TestLsServerKeepsSectionLocalStaysFlat(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "localbox/dabs-node.json":  []byte(`{"id":"localbox","kind":"box","instance":"inst-local","created":"1"}`),
		base + "remotebox/dabs-node.json": []byte(`{"id":"remotebox","kind":"box","instance":"inst-remote","created":"2"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"localbox", "remotebox"}}
	local := &fakeDriver{kind: "apple", infos: []sandbox.Info{{Name: "inst-local", Status: "running"}}}
	server := &fakeDriver{kind: "ssh", infos: []sandbox.Info{{Name: "inst-remote", Status: "running"}}}
	r := actions.New(
		map[string]sandbox.Driver{"local": local, "homelab": server},
		[]string{"local", "homelab"}, fstest.MapFS{}, fd,
	)

	out := captureStdout(t, func() {
		if err := r.Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	if l := lineWith(out, "localbox"); !strings.Contains(l, "box (apple)") {
		t.Fatalf("the local box must carry its driver tag:\n%s", out)
	}
	if l := lineWith(out, "remotebox"); strings.Contains(l, "box (") {
		t.Fatalf("a box under a server section must not be double-tagged:\n%s", out)
	}
	// Position is the real contract: the local box sits in the flat tree ABOVE
	// the server heading, and the server's box sits UNDER that heading. A
	// substring check alone passes even if servers collapse into the flat tree
	// (the heading still prints for an empty section, and an ssh-kind box carries
	// no tag) — so assert the ordering localbox < heading < remotebox.
	localIdx := strings.Index(out, "localbox")
	headingIdx := strings.Index(out, "homelab (ssh)")
	remoteIdx := strings.Index(out, "remotebox")
	if localIdx < 0 || remoteIdx < 0 {
		t.Fatalf("both boxes must render:\n%s", out)
	}
	if headingIdx < 0 {
		t.Fatalf("the server must keep its own heading:\n%s", out)
	}
	if !(localIdx < headingIdx) {
		t.Fatalf("the local box must sit in the flat tree, above the server heading:\n%s", out)
	}
	if !(headingIdx < remoteIdx) {
		t.Fatalf("the server's box must sit under its heading, not in the flat local tree:\n%s", out)
	}
}

// CONTRACT: a project whose source repo differs from its record dir gets a
// muted (location) row — KIND (location), WHERE the repo, STATE the repo's git
// signal. A dirty repo shows its signal; a clean one on main shows just `main`.
func TestLsLocationRowProject(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj/dabs-node.json": []byte(`{"id":"proj","kind":"project","dir":"/repo","created":"1"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj"}}
	spaceHeld(fd, "proj", "held") // real files → the subtree is ACTIVE and shows
	fd.prompts = map[string]data.GitPrompt{"/repo": {Branch: "main", Unstaged: true, Untracked: true}}

	out := captureStdout(t, func() {
		if err := actions.New(map[string]sandbox.Driver{"local": &fakeDriver{}}, []string{"local"}, fstest.MapFS{}, fd).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})

	loc := lineWith(out, "(location)")
	if loc == "" {
		t.Fatalf("no (location) row for a project whose repo differs from its node dir:\n%s", out)
	}
	if !strings.Contains(loc, "/repo") {
		t.Fatalf("(location) WHERE must be the source repo:\n%s", loc)
	}
	if !strings.Contains(loc, "main *%") {
		t.Fatalf("(location) STATE must carry the repo's git signal:\n%s", loc)
	}
	// The `%` untracked glyph must reach the terminal literally, not be read as a
	// format verb: the STATE cell renders the signal as data (`%s`), never as a
	// format string. A `Contains("main *%")` check alone would pass a mangled
	// `main *%!(NOVERB)`, so guard the tail explicitly.
	if strings.Contains(loc, "NOVERB") || strings.Contains(loc, "%!") {
		t.Fatalf("git signal was rendered as a format string (%% treated as a verb):\n%s", loc)
	}
}

// CONTRACT: a clean repo on main with no divergence reduces to just the branch
// name in the (location) STATE — no glyphs when there is nothing to say.
func TestLsLocationRowCleanRepo(t *testing.T) {
	base := "/home/t/.dabs/nodes/"
	fd := baseData()
	fd.files = map[string][]byte{
		base + "proj/dabs-node.json": []byte(`{"id":"proj","kind":"project","dir":"/repo","created":"1"}`),
	}
	fd.dirs = map[string][]string{"/home/t/.dabs/nodes": {"proj"}}
	spaceHeld(fd, "proj", "held")
	fd.prompts = map[string]data.GitPrompt{"/repo": {Branch: "main"}}

	out := captureStdout(t, func() {
		if err := actions.New(map[string]sandbox.Driver{"local": &fakeDriver{}}, []string{"local"}, fstest.MapFS{}, fd).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})
	loc := lineWith(out, "(location)")
	if loc == "" || !strings.Contains(loc, "main") {
		t.Fatalf("clean repo (location) row must read just `main`:\n%s", out)
	}
	if strings.ContainsAny(loc, "*+%") {
		t.Fatalf("clean repo (location) must carry no dirty glyphs:\n%s", loc)
	}
}

// CONTRACT: a worktree gets a (location) row pointing at its checkout, and its
// STATE is the checkout's git signal. A checkout that is not a git repo (no
// prompt) leaves the STATE blank rather than guessing.
func TestLsLocationRowWorktreeNonGit(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "wt-abcd", wtState{branch: "dabs/wt-abcd"})
	spaceHeld(fd, "wt-abcd", "held") // active so it lists
	// fd.prompts is nil: GitPromptStatus errors, like a non-git checkout.

	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})
	loc := lineWith(out, "(location)")
	if loc == "" {
		t.Fatalf("a worktree must get a (location) row for its checkout:\n%s", out)
	}
	if !strings.Contains(loc, "/wt-abcd/data") {
		t.Fatalf("(location) WHERE must be the checkout path:\n%s", loc)
	}
}

// CONTRACT: a box never gets a (location) row — it has no working place of its
// own, its place IS its node dir.
func TestLsNoLocationRowForBox(t *testing.T) {
	fd := baseData()
	seedBoxNode(fd, "boxy", "inst-b")
	drv := &fakeDriver{infos: []sandbox.Info{{Name: "inst-b", Status: "running"}}}
	out := captureStdout(t, func() {
		if err := newReal("", fd, drv).Ls(params.Ls{}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
	})
	if strings.Contains(out, "(location)") {
		t.Fatalf("a lone box must not produce a (location) row:\n%s", out)
	}
}
