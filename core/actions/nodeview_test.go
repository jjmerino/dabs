package actions

// Unit tests for the view-model layer: viewNodes turns node records into a tree
// of display cells with NO driver query, and renderForest draws exactly the
// columns it is handed. The fakes here are deliberately tiny — viewNodes reads
// only spaces (local), the passed box-state map, and local git, so a fake that
// answers those three is enough to pin the whole contract.

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/sandbox"
)

// vFakeData answers the handful of reads viewNodes makes. Everything else is a
// zero-valued stub: if viewNodes ever grew a new dependency, the stub would
// return nothing and the test would notice.
type vFakeData struct {
	held   map[string]bool // ReadDir path -> non-empty (a held space)
	statOK map[string]bool // Stat path -> exists
	git    map[string]vGit // GitState by worktree path
}

type vGit struct {
	dirty bool
	ahead int
}

func (f *vFakeData) HomeDir() (string, error) { return "/home/t", nil }
func (f *vFakeData) ReadDir(dir string) ([]string, error) {
	// A held space lists one child that is itself a file: listing that child
	// errors (ENOTDIR), the way the OS reports a non-directory.
	if strings.HasSuffix(dir, "/__file__") {
		return nil, fs.ErrInvalid
	}
	if f.held[dir] {
		return []string{"__file__"}, nil
	}
	return nil, nil
}
func (f *vFakeData) Stat(path string) (fs.FileInfo, error) {
	if f.statOK[path] {
		return nil, nil
	}
	return nil, fs.ErrNotExist
}
func (f *vFakeData) GitState(wt string) (string, bool, int, error) {
	g := f.git[wt]
	return "", g.dirty, g.ahead, nil
}

// The rest of data.Data is unused by viewNodes; stub it out.
func (f *vFakeData) ReadFile(string) ([]byte, error)              { return nil, fs.ErrNotExist }
func (f *vFakeData) WriteFile(string, []byte, fs.FileMode) error  { return nil }
func (f *vFakeData) AppendFile(string, []byte, fs.FileMode) error { return nil }
func (f *vFakeData) MkdirAll(string, fs.FileMode) error           { return nil }
func (f *vFakeData) Mkdir(string, fs.FileMode) error              { return nil }
func (f *vFakeData) MkdirTemp(string, string) (string, error)     { return "", nil }
func (f *vFakeData) RemoveAll(string) error                       { return nil }
func (f *vFakeData) CopyDir(string, string) error                 { return nil }
func (f *vFakeData) Getenv(string) string                         { return "" }
func (f *vFakeData) Getwd() (string, error)                       { return "", nil }
func (f *vFakeData) LookupEnv(string) (string, bool)              { return "", false }
func (f *vFakeData) ExpandEnv(s string) string                    { return s }
func (f *vFakeData) GitToplevel(string) (string, error)           { return "", fs.ErrNotExist }
func (f *vFakeData) GitHasCommits(string) bool                    { return false }
func (f *vFakeData) GitAddWorktree(string, string, string) error  { return nil }
func (f *vFakeData) GitDiff(string) (string, error)               { return "", nil }
func (f *vFakeData) GitRemoveWorktree(string) error               { return nil }
func (f *vFakeData) GitCommonDir(string) (string, error)          { return "", fs.ErrNotExist }

// panicDriver proves laziness: viewNodes must never reach a driver, so any call
// that would (Ls above all) blows up the test loudly.
type panicDriver struct{}

func (panicDriver) Build(sandbox.BuildSpec) error         { panic("driver touched") }
func (panicDriver) HasImage(string) (bool, error)         { panic("driver touched") }
func (panicDriver) Up(sandbox.Spec) (string, error)       { panic("driver touched") }
func (panicDriver) Run(string, []string) error            { panic("driver touched") }
func (panicDriver) Exec(string, []string) (string, error) { panic("driver touched") }
func (panicDriver) Down(string) error                     { panic("driver touched") }
func (panicDriver) Ls() ([]sandbox.Info, error)           { panic("driver touched") }
func (panicDriver) Kind() string                          { return "panic" }

func newViewReal(fd *vFakeData) Real {
	drivers := map[string]sandbox.Driver{"local": panicDriver{}}
	return New(drivers, []string{"local"}, fstest.MapFS{}, fd)
}

const vBase = "/home/t/.dabs/nodes/"

// A box's state is read ONLY from the passed map — present is live, absent is
// gone — and building the view for a box the map omits contacts no driver. The
// panicDriver is the proof: a stray query would crash this.
func TestViewNodesBoxStateFromMapOnly(t *testing.T) {
	nodes := []Node{
		{ID: "box-live", Kind: KindBox, Instance: "inst-live", Created: "1"},
		{ID: "box-gone", Kind: KindBox, Instance: "inst-gone", Created: "2"},
	}
	state := map[string]boxState{"inst-live": {status: "running", where: "local"}}

	roots := newViewReal(&vFakeData{}).viewNodes(nodes, state)

	byID := map[string]*NodeView{}
	for _, v := range roots {
		byID[v.ID] = v
	}
	if got := byID["box-live"].State; got != CellLive {
		t.Errorf("live box State = %v, want CellLive", got)
	}
	if got := byID["box-gone"].State; got != CellGone {
		t.Errorf("gone box State = %v, want CellGone", got)
	}
	// WHERE points at the box's node dir on disk AND still carries the instance
	// name, so a box's bytes are locatable while rm/exec still resolve it.
	if w := byID["box-live"].Where; !strings.Contains(w, ".dabs/nodes/box-live") || !strings.Contains(w, "inst-live") {
		t.Errorf("box Where = %q, want the node dir and the instance name", w)
	}
}

// The tree is built from parent links WITHIN the set: a child whose parent is
// present nests under it; a node whose parent is absent is a root.
func TestViewNodesBuildsTreeFromParentLinks(t *testing.T) {
	nodes := []Node{
		{ID: "proj", Kind: KindProject, Dir: "/repo", Created: "1"},
		{ID: "box", Kind: KindBox, Parent: "proj", Instance: "inst", Created: "2"},
		{ID: "orphan", Kind: KindBox, Parent: "not-in-set", Instance: "loose", Created: "3"},
	}
	roots := newViewReal(&vFakeData{}).viewNodes(nodes, nil)

	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2 (proj and the orphan)", len(roots))
	}
	// Oldest-first: proj (created 1) before orphan (created 3).
	if roots[0].ID != "proj" || roots[1].ID != "orphan" {
		t.Fatalf("root order = [%s %s], want [proj orphan]", roots[0].ID, roots[1].ID)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].ID != "box" {
		t.Fatalf("proj children = %+v, want [box]", roots[0].Children)
	}
}

// Space cells reflect whether the node's OWN space holds anything: a held space
// warns, an empty one is clean. Spaces are read locally for every kind.
func TestViewNodesSpaceCells(t *testing.T) {
	fd := &vFakeData{held: map[string]bool{
		vBase + "proj/volume": true,
	}}
	nodes := []Node{{ID: "proj", Kind: KindProject, Dir: "/repo", Created: "1"}}

	v := newViewReal(fd).viewNodes(nodes, nil)[0]
	if v.Volume != CellHeld {
		t.Errorf("held volume = %v, want CellHeld", v.Volume)
	}
	if v.Ephemeral != CellEmpty || v.Tmp != CellEmpty {
		t.Errorf("empty spaces = eph %v tmp %v, want both CellEmpty", v.Ephemeral, v.Tmp)
	}
}

// A worktree's State is its local git state: dirty or ahead is unmerged work,
// clean is merged.
func TestViewNodesWorktreeState(t *testing.T) {
	dirtyWT := vBase + "wt-dirty/ephemeral/worktree"
	cleanWT := vBase + "wt-clean/ephemeral/worktree"
	fd := &vFakeData{
		statOK: map[string]bool{dirtyWT: true, cleanWT: true},
		git:    map[string]vGit{dirtyWT: {dirty: true}},
	}
	nodes := []Node{
		{ID: "wt-dirty", Kind: KindWorktree, Worktree: &NodeWorktree{Branch: "dabs/d"}, Created: "1"},
		{ID: "wt-clean", Kind: KindWorktree, Worktree: &NodeWorktree{Branch: "dabs/c"}, Created: "2"},
	}
	roots := newViewReal(fd).viewNodes(nodes, nil)
	byID := map[string]*NodeView{}
	for _, v := range roots {
		byID[v.ID] = v
	}
	if byID["wt-dirty"].State != CellUnmerged {
		t.Errorf("dirty worktree State = %v, want CellUnmerged", byID["wt-dirty"].State)
	}
	if byID["wt-clean"].State != CellNoDiff {
		t.Errorf("clean worktree State = %v, want CellNoDiff", byID["wt-clean"].State)
	}
	// Where points at the checkout folder on disk, not a recorded source.
	if !strings.Contains(byID["wt-dirty"].Where, "wt-dirty/ephemeral/worktree") {
		t.Errorf("worktree Where = %q, want the checkout folder", byID["wt-dirty"].Where)
	}
}

// renderForest draws exactly the requested columns, the tree glyphs for nested
// nodes, and the space legend only when a space column is present.
func TestRenderForestColumnsAndGlyphs(t *testing.T) {
	proj := &NodeView{ID: "proj", Kind: KindProject, State: CellNA, Where: "/repo"}
	box := &NodeView{ID: "box", Kind: KindBox, State: CellLive, Where: "inst",
		Volume: CellHeld, Ephemeral: CellEmpty, Tmp: CellEmpty}
	proj.Children = []*NodeView{box}

	out := renderForest([]*NodeView{proj}, []Column{ColNode, ColKind, ColVol, ColState}, 2)

	for _, want := range []string{"NODE", "KIND", "VOL", "STATE", "proj", "box", "live", "└─ "} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "✓ empty  ⚠ holds files") {
		t.Errorf("space legend missing when a space column is drawn:\n%s", out)
	}
	// A column not asked for is not drawn.
	if strings.Contains(out, "WHERE") {
		t.Errorf("WHERE drawn though not requested:\n%s", out)
	}

	// With no space column, no legend.
	noSpace := renderForest([]*NodeView{proj}, []Column{ColNode, ColState}, 0)
	if strings.Contains(noSpace, "holds files") {
		t.Errorf("legend drawn without a space column:\n%s", noSpace)
	}
}

// CONTRACT: a node id or a WHERE path is untrusted display data — it can carry a
// newline (splitting one row into phantom tree lines) or an ANSI escape (moving
// the cursor / spoofing the terminal). renderForest must neutralize both before
// drawing: no raw ESC (0x1b) or newline survives from the value, and the row
// stays a single line.
func TestRenderForestSanitizesUntrustedFields(t *testing.T) {
	proj := &NodeView{
		ID:    "ev\x1b[31mil\nid",
		Kind:  KindProject,
		State: CellNA,
		Where: "/re\x1b[2Jpo\nboom",
	}

	out := renderForest([]*NodeView{proj}, []Column{ColNode, ColWhere}, 0)

	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("raw ESC (0x1b) survived into rendered output:\n%q", out)
	}
	// The header line plus exactly one node row — no phantom rows from the
	// embedded newlines.
	if lines := strings.Count(strings.TrimRight(out, "\n"), "\n"); lines != 1 {
		t.Errorf("want 1 newline (header + one row), got %d:\n%q", lines, out)
	}
	// The inert letters of the neutralized sequences remain as plain text.
	if !strings.Contains(out, "il") || !strings.Contains(out, "id") {
		t.Errorf("sanitized id lost its printable text:\n%q", out)
	}
}

// A workdir's Where is its OWN on-disk folder (where the copy lives), not the
// recorded source — which for a workdir is the parent project's directory. This
// resolves from storage, so it is right even for records that stored the source.
func TestViewNodesWorkdirWhereIsItsCopyFolder(t *testing.T) {
	work := vBase + "wd/ephemeral/work"
	fd := &vFakeData{statOK: map[string]bool{work: true}}
	// Dir records the SOURCE (the project) — the pre-fix value; Where must not use it.
	nodes := []Node{{ID: "wd", Kind: KindWorkdir, Dir: "/some/repo", Created: "1"}}

	v := newViewReal(fd).viewNodes(nodes, nil)[0]
	if !strings.Contains(v.Where, "wd/ephemeral/work") {
		t.Errorf("workdir Where = %q, want its copy folder", v.Where)
	}
	if strings.Contains(v.Where, "/some/repo") {
		t.Errorf("workdir Where leaked the source path: %q", v.Where)
	}
}

// countHeldSpaces walks the whole forest (into Children) and tallies data per
// space — the aggregate a cascade reap asks about once.
func TestCountHeldSpaces(t *testing.T) {
	roots := []*NodeView{
		{Ephemeral: CellHeld, Children: []*NodeView{
			{Ephemeral: CellHeld, Volume: CellHeld},
			{Tmp: CellHeld},
		}},
		{Volume: CellHeld},
	}
	eph, vol, tmp := countHeldSpaces(roots)
	if eph != 2 || vol != 2 || tmp != 1 {
		t.Errorf("counts = eph %d vol %d tmp %d, want 2/2/1", eph, vol, tmp)
	}
}

// treeData models an explicit directory tree for spaceHolds: dirs maps a path to
// its child names; a path in files is a non-directory, so listing it errors the
// way the OS does with ENOTDIR.
type treeData struct {
	*vFakeData
	dirs  map[string][]string
	files map[string]bool
}

func (t *treeData) ReadDir(dir string) ([]string, error) {
	if t.files[dir] {
		return nil, fs.ErrInvalid
	}
	return t.dirs[dir], nil // absent path -> nil, nil
}

// CONTRACT (E2-4): a space whose tree is only empty subdirectories holds
// nothing; it becomes held only once a real file appears anywhere in the tree.
func TestSpaceHoldsIgnoresEmptySubdirs(t *testing.T) {
	empty := &treeData{vFakeData: &vFakeData{}, dirs: map[string][]string{
		"space":     {"a"},
		"space/a":   {"b"},
		"space/a/b": {},
	}}
	if held, err := (Real{data: empty}).spaceHolds("space"); err != nil || held {
		t.Fatalf("space of only empty subdirs: held=%v err=%v, want false/nil", held, err)
	}

	withFile := &treeData{vFakeData: &vFakeData{},
		dirs:  map[string][]string{"space": {"a"}, "space/a": {"b"}, "space/a/b": {"f"}},
		files: map[string]bool{"space/a/b/f": true},
	}
	if held, err := (Real{data: withFile}).spaceHolds("space"); err != nil || !held {
		t.Fatalf("real file deep in tree: held=%v err=%v, want true/nil", held, err)
	}
}
