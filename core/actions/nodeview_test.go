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
	if f.held[dir] {
		return []string{"a-file"}, nil
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
	if byID["box-live"].Where != "inst-live" {
		t.Errorf("box Where = %q, want the instance name", byID["box-live"].Where)
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
	if !strings.Contains(out, "✓ empty  ⚠ holds files  — n/a") {
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
