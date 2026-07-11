package actions_test

// Component tests for the recipe engine: the whole Real.Recipe orchestration is
// driven through its public API with the two seams faked — sandbox.Driver and
// data.Data. Assertions are written from the CONTRACT (what a recipe should
// cause), not by mirroring the implementation, so they can actually fail.

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// Compile-time proof the fakes satisfy the real seams.
var (
	_ sandbox.Driver = (*fakeDriver)(nil)
	_ data.Data      = (*fakeData)(nil)
)

// --- fake driver: records every op, returns canned results -------------------

type fakeDriver struct {
	built  map[string]bool // name -> HasImage answer
	builds []sandbox.BuildSpec
	ups    []sandbox.Spec
	upErr  error
	execs  [][]string
	runs   [][]string
	runErr error
	downs  []string
	nInst  int
}

func (d *fakeDriver) Build(s sandbox.BuildSpec) error { d.builds = append(d.builds, s); return nil }
func (d *fakeDriver) HasImage(name string) (bool, error) {
	return d.built[name], nil
}
func (d *fakeDriver) Up(s sandbox.Spec) (string, error) {
	if d.upErr != nil {
		return "", d.upErr
	}
	d.ups = append(d.ups, s)
	d.nInst++
	return s.Name + "-inst", nil
}
func (d *fakeDriver) Run(_ string, cmd []string) error { d.runs = append(d.runs, cmd); return d.runErr }
func (d *fakeDriver) Exec(_ string, cmd []string) (string, error) {
	d.execs = append(d.execs, cmd)
	return "", nil
}
func (d *fakeDriver) Down(inst string) error      { d.downs = append(d.downs, inst); return nil }
func (d *fakeDriver) Ls() ([]sandbox.Info, error) { return nil, nil }
func (d *fakeDriver) Kind() string                { return "fake" }

// --- fake data: canned fs/env/git, records mutations -------------------------

type fakeData struct {
	home      string
	env       map[string]string
	files     map[string][]byte // ReadFile
	exists    map[string]bool   // Stat -> exists
	isDir     map[string]bool   // Stat -> IsDir (subset of exists)
	toplevel  map[string]error  // GitToplevel: dir present with nil err => repo root is the dir
	noCommits map[string]bool   // GitHasCommits false for these tops
	worktrees []string          // recorded GitAddWorktree dests
	mkdirs    []string
	dirs      map[string][]string // ReadDir results
	states    map[string]wtState  // GitState by worktree path
	removed   []string            // recorded GitRemoveWorktree
	commondir map[string]string   // GitCommonDir: worktree path -> parent .git (present => a worktree)
}

type wtState struct {
	branch string
	dirty  bool
	ahead  int
}

func (f *fakeData) HomeDir() (string, error) { return f.home, nil }
func (f *fakeData) ReadFile(p string) ([]byte, error) {
	if b, ok := f.files[p]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}
func (f *fakeData) WriteFile(string, []byte, fs.FileMode) error { return nil }
func (f *fakeData) Stat(p string) (fs.FileInfo, error) {
	if f.exists[p] {
		if f.isDir[p] {
			return dirFileInfo{}, nil
		}
		return nil, nil
	}
	return nil, fs.ErrNotExist
}

// dirFileInfo is a minimal fs.FileInfo reporting IsDir()==true, so Stat can
// stand in for a directory (the `dabs build <dir>` resolution branch).
type dirFileInfo struct{}

func (dirFileInfo) Name() string       { return "" }
func (dirFileInfo) Size() int64        { return 0 }
func (dirFileInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (dirFileInfo) ModTime() time.Time { return time.Time{} }
func (dirFileInfo) IsDir() bool        { return true }
func (dirFileInfo) Sys() any           { return nil }
func (f *fakeData) MkdirAll(p string, _ fs.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeData) MkdirTemp(string, string) (string, error) { return "/tmp/x", nil }
func (f *fakeData) RemoveAll(string) error                   { return nil }
func (f *fakeData) Getenv(k string) string                   { return f.env[k] }
func (f *fakeData) LookupEnv(k string) (string, bool)        { v, ok := f.env[k]; return v, ok }
func (f *fakeData) ExpandEnv(s string) string {
	return os.Expand(s, func(k string) string { return f.env[k] })
}
func (f *fakeData) GitToplevel(dir string) (string, error) {
	if err, ok := f.toplevel[dir]; ok {
		if err != nil {
			return "", err
		}
		return dir, nil
	}
	return "", errors.New("not a git repository")
}
func (f *fakeData) GitHasCommits(top string) bool { return !f.noCommits[top] }
func (f *fakeData) GitAddWorktree(_, _, dest string) error {
	f.worktrees = append(f.worktrees, dest)
	return nil
}
func (f *fakeData) ReadDir(dir string) ([]string, error) { return f.dirs[dir], nil }
func (f *fakeData) GitState(wt string) (string, bool, int, error) {
	s := f.states[wt]
	return s.branch, s.dirty, s.ahead, nil
}
func (f *fakeData) GitDiff(wt string) (string, error) { return "diff of " + wt, nil }
func (f *fakeData) GitRemoveWorktree(wt string) error {
	f.removed = append(f.removed, wt)
	return nil
}
func (f *fakeData) GitCommonDir(wt string) (string, error) {
	if g, ok := f.commondir[wt]; ok {
		return g, nil
	}
	return "", errors.New("not a git worktree")
}

// --- harness ------------------------------------------------------------------

// build a Real wired to the fakes, with the given user recipes.yaml and an
// images FS advertising the named bundled images.
func newReal(recipesYAML string, fd *fakeData, drv *fakeDriver, bundledImages ...string) actions.Real {
	imgs := fstest.MapFS{}
	for _, n := range bundledImages {
		imgs["images/"+n] = &fstest.MapFile{Mode: fs.ModeDir}
	}
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	if recipesYAML != "" {
		fd.files[fd.home+"/.dabs/recipes.yaml"] = []byte(recipesYAML)
	}
	drivers := map[string]sandbox.Driver{"local": drv}
	return actions.New(drivers, []string{"local"}, imgs, fd)
}

func baseData() *fakeData {
	return &fakeData{home: "/home/t", env: map[string]string{}, exists: map[string]bool{}, isDir: map[string]bool{}, toplevel: map[string]error{}, noCommits: map[string]bool{}}
}

func onlyUp(t *testing.T, d *fakeDriver) sandbox.Spec {
	t.Helper()
	if len(d.ups) != 1 {
		t.Fatalf("want exactly one Up, got %d", len(d.ups))
	}
	return d.ups[0]
}

// --- tests: happy paths -------------------------------------------------------

func TestRecipeMountReachesDriver(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [run, it]
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
	up := onlyUp(t, drv)
	// Contract: the box is brought up with exactly the declared mount, its
	// command is run, and it is torn down.
	if len(up.Mounts) != 1 || up.Mounts[0] != (sandbox.Mount{Host: "/data", Path: "/work"}) {
		t.Errorf("Up mounts = %+v, want one {/data -> /work}", up.Mounts)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "run it" {
		t.Errorf("Run cmd = %v, want [run it]", drv.runs)
	}
	if len(drv.downs) != 1 {
		t.Errorf("Down not called once: %v", drv.downs)
	}
}

func TestRecipeTildeExpansion(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: ~/vault
        path: /root/.cfg
`
	fd := baseData()
	fd.exists["/home/t/vault"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if got := onlyUp(t, drv).Mounts[0].Host; got != "/home/t/vault" {
		t.Errorf("~ not expanded: host = %q, want /home/t/vault", got)
	}
}

func TestRecipeWorktreeCreatesAndMounts(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData()
	fd.toplevel["."] = nil // "." is a repo whose root is "."
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "w"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(fd.worktrees) != 1 {
		t.Fatalf("want one worktree created, got %v", fd.worktrees)
	}
	// Contract: the box mounts the worktree that was created, at the declared path.
	up := onlyUp(t, drv)
	if up.Mounts[0].Host != fd.worktrees[0] || up.Mounts[0].Path != "/work" {
		t.Errorf("Up mount = %+v, want the created worktree at /work", up.Mounts[0])
	}
}

func TestRecipeCopyStagesThenCopies(t *testing.T) {
	y := `recipes:
  c:
    image: img
    command: [x]
    sources:
      - copy: /src
        path: /work
`
	fd := baseData()
	fd.exists["/src"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "c"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	// Contract: copy is a snapshot — the box is NOT given a live mount at /work;
	// instead the source is staged read-only and copied into /work after up.
	up := onlyUp(t, drv)
	for _, m := range up.Mounts {
		if m.Path == "/work" && !m.RO {
			t.Errorf("copy source produced a writable mount at /work: %+v", m)
		}
	}
	// Snapshot is materialized by argv Execs (mkdir + cp) — no shell string, so a
	// quirky dest path can't break it.
	if len(drv.execs) != 2 {
		t.Fatalf("want mkdir+cp Execs, got %v", drv.execs)
	}
	if drv.execs[0][0] != "mkdir" || drv.execs[1][0] != "cp" {
		t.Errorf("copy execs = %v, want [mkdir…] then [cp…]", drv.execs)
	}
	if last := drv.execs[1]; last[len(last)-1] != "/work" {
		t.Errorf("cp dest = %v, want /work", last)
	}
}

// --- tests: error paths that must NOT touch the box --------------------------

func TestRecipeUnknownErrors(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{}
	err := newReal("", fd, drv).Recipe(params.Recipe{Name: "nope"})
	if err == nil || !strings.Contains(err.Error(), "no recipe") {
		t.Fatalf("want unknown-recipe error, got %v", err)
	}
}

func TestRecipeNoCommandErrors(t *testing.T) {
	y := `recipes:
  x:
    image: img
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "no command") {
		t.Fatalf("want no-command error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("no-command recipe still brought a box up: %v", drv.ups)
	}
}

func TestRecipeAmbiguousSourceErrors(t *testing.T) {
	y := `recipes:
  x:
    image: img
    command: [x]
    sources:
      - mount: /a
        copy: /b
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("want ambiguous-source error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("invalid recipe still brought a box up: %v", drv.ups)
	}
}

func TestRecipeWorktreeNonGitFailsBeforeBuild(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData() // "." not registered as a repo -> GitToplevel errors
	drv := &fakeDriver{built: map[string]bool{"img": false}}
	err := newReal(y, fd, drv, "img").Recipe(params.Recipe{Name: "w"})
	if err == nil || !strings.Contains(err.Error(), "not a git") {
		t.Fatalf("want non-git error, got %v", err)
	}
	// Contract: validate before side effects — no image build, no box.
	if len(drv.builds) != 0 || len(drv.ups) != 0 {
		t.Errorf("side effects before validation failed: builds=%v ups=%v", drv.builds, drv.ups)
	}
}

// CONTRACT: a worktree recipe on a repo with no commits must fail WITHOUT any
// side effect — same as the non-git case. (Bug hunt: the commit check happens
// inside worktree creation, which runs after the image is ensured.)
func TestRecipeWorktreeNoCommitsFailsBeforeBuild(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData()
	fd.toplevel["."] = nil                                   // "." IS a repo...
	fd.noCommits["."] = true                                 // ...but has no commits
	drv := &fakeDriver{built: map[string]bool{"img": false}} // not built -> build WOULD happen
	err := newReal(y, fd, drv, "img").Recipe(params.Recipe{Name: "w"})
	if err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("want no-commits error, got %v", err)
	}
	if len(drv.builds) != 0 || len(drv.ups) != 0 {
		t.Errorf("side effects before no-commits validation: builds=%v ups=%v", drv.builds, drv.ups)
	}
}

// CONTRACT: a mount whose host does not exist must fail clearly BEFORE up, not
// hand a phantom path to the driver. (Bug hunt / vault regression: `dabs recipe
// claude` before `dabs auth claude` used to warn+create; now the mount host is
// missing and this should be a clear recipe-level error, not a driver crash.)
func TestRecipeMountMissingSourceFailsClearly(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /home/t/.dabs/auth/claude
        path: /root/.claude
`
	fd := baseData() // the auth vault does NOT exist
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil {
		t.Fatalf("want a clear error for a missing mount source; got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should name the missing source: %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("brought a box up with a nonexistent mount source: %v", drv.ups)
	}
}

// CONTRACT: an unset variable in a source path is a mistake, not a silent
// truncation to a shorter (wrong) path. (Bug hunt: os.ExpandEnv turns "$NOPE/x"
// into "/x".)
func TestRecipeUnsetVarInPathIsAnError(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: $NOPE/vault
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil {
		t.Fatalf("want an error for an unset variable in a source path; got nil (mount host would be %q)", "/vault")
	}
	if len(drv.ups) != 0 {
		t.Errorf("brought a box up with an under-expanded path: %v", drv.ups)
	}
}

// CONTRACT: an image that isn't built and has no bundled recipe fails clearly,
// without bringing a box up.
func TestRecipeUnknownImageErrors(t *testing.T) {
	y := `recipes:
  m:
    image: ghost
    command: [x]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{}} // not built, and no bundled "ghost"
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "not built") {
		t.Fatalf("want unknown-image error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("brought a box up with an unresolvable image: %v", drv.ups)
	}
}

// CONTRACT: `dabs recipe` with no name runs the project dabs.yaml's default.
func TestRecipeRunsLocalDefault(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{"dabs.yaml": []byte(`default: dev
recipes:
  dev:
    image: img
    command: [devcmd]
    sources:
      - mount: /d
        path: /work
`)}
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal("", fd, drv).Recipe(params.Recipe{}); err != nil { // no name
		t.Fatalf("default recipe: %v", err)
	}
	if len(drv.runs) != 1 || drv.runs[0][0] != "devcmd" {
		t.Errorf("default not run: %v", drv.runs)
	}
}

// CONTRACT: no name and no default is an error that forces a choice — never a
// silent pick.
func TestRecipeNoNameNoDefaultErrors(t *testing.T) {
	fd := baseData()
	err := newReal("", fd, &fakeDriver{}).Recipe(params.Recipe{})
	if err == nil || !strings.Contains(err.Error(), "no default") {
		t.Fatalf("want no-default error, got %v", err)
	}
}

// CONTRACT: a project dabs.yaml recipe overrides a global one of the same name.
func TestLocalRecipeOverridesGlobal(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{
		"/home/t/.dabs/recipes.yaml": []byte("recipes:\n  x:\n    image: img\n    command: [fromglobal]\n"),
		"dabs.yaml":                  []byte("recipes:\n  x:\n    image: img\n    command: [fromlocal]\n"),
	}
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal("", fd, drv).Recipe(params.Recipe{Name: "x"}); err != nil {
		t.Fatalf("recipe x: %v", err)
	}
	if len(drv.runs) != 1 || drv.runs[0][0] != "fromlocal" {
		t.Errorf("local recipe did not win: %v", drv.runs)
	}
}

// CONTRACT: the box is always torn down, even when its command fails.
func TestRecipeDownEvenWhenRunFails(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [boom]
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}, runErr: errors.New("boom")}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil {
		t.Fatalf("want the run error surfaced")
	}
	if len(drv.downs) != 1 {
		t.Errorf("box not torn down after a failed run: downs=%v", drv.downs)
	}
}

// --- tests: cast (bind a recipe onto an existing worktree) --------------------

// CONTRACT: `dabs cast <recipe> <worktree>` on a `worktree: .` recipe ATTACHES
// the existing worktree (mounts it live, never forks a new branch) and also
// mounts the parent .git so git resolves in-box. Non-`.` sources pass through.
func TestCastAttachesWorktreeAndGitDir(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - mount: ~/vault
        path: /root/.cfg
      - worktree: .
        path: /work
`
	fd := baseData()
	wt := "/home/t/.dabs/worktrees/wt1"
	fd.exists[wt] = true
	fd.exists["/home/t/vault"] = true
	fd.exists["/repo/.git"] = true // parent store exists (git rev-parse yields a real path)
	fd.commondir = map[string]string{wt: "/repo/.git"}
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "w", Worktree: "wt1"}); err != nil {
		t.Fatalf("cast: %v", err)
	}
	// It must NOT fork a fresh worktree.
	if len(fd.worktrees) != 0 {
		t.Fatalf("cast forked a worktree, want none: %v", fd.worktrees)
	}
	up := onlyUp(t, drv)
	want := []sandbox.Mount{
		{Host: "/home/t/vault", Path: "/root/.cfg"}, // passthrough
		{Host: wt, Path: "/work"},                   // the worktree, attached live
		{Host: "/repo/.git", Path: "/repo/.git"},    // parent store, so git works in-box
	}
	if len(up.Mounts) != len(want) {
		t.Fatalf("mounts = %+v, want %+v", up.Mounts, want)
	}
	for i := range want {
		if up.Mounts[i] != want[i] {
			t.Errorf("mount[%d] = %+v, want %+v", i, up.Mounts[i], want[i])
		}
	}
}

// CONTRACT: casting a recipe that has no `.` source is a user error, not a
// silent no-op — there's nothing to bind the worktree to.
func TestCastRecipeWithoutDotSourceErrors(t *testing.T) {
	y := `recipes:
  v:
    image: img
    command: [x]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	wt := "/home/t/.dabs/worktrees/wt1"
	fd.exists[wt] = true
	fd.commondir = map[string]string{wt: "/repo/.git"}
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "v", Worktree: "wt1"})
	if err == nil || !strings.Contains(err.Error(), "no `.` source") {
		t.Fatalf("want 'no `.` source' error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite bad cast: %v", drv.ups)
	}
}

// CONTRACT: casting onto a missing worktree fails cleanly before any box work.
func TestCastMissingWorktreeErrors(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "w", Worktree: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("want 'no worktree' error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite missing worktree: %v", drv.ups)
	}
}

// --- tests: the generic perbox source -----------------------------------------

// CONTRACT: a `perbox:` source yields a fresh, empty, box-private host mount
// (under ~/.dabs/boxes/…/<label>) at its Path, created on the host, writable,
// and — since it exists to overlay an earlier mount — applied AFTER any mount it
// nests over, regardless of its position among the recipe's sources.
func TestPerboxSourceIsPrivateAndAppliedLast(t *testing.T) {
	// The perbox source is declared BEFORE the mount it nests over, to prove the
	// engine still applies it LAST (overlay ordering is not source order).
	y := `recipes:
  r:
    image: img
    command: [run]
    sources:
      - mount: /home/t/vault
        path: /cfg
      - perbox: sessions
        path: /cfg/sessions
      - mount: /work
        path: /work
`
	fd := baseData()
	fd.exists["/home/t/vault"] = true
	fd.exists["/work"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "r"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	up := onlyUp(t, drv)

	// The perbox mount is applied LAST so it lands over the /cfg mount it nests in.
	pb := up.Mounts[len(up.Mounts)-1]
	if pb.Path != "/cfg/sessions" {
		t.Fatalf("last mount = %+v, want the perbox mount at /cfg/sessions", pb)
	}
	// It is a fresh box-private host dir, not a shared origin.
	if !strings.HasPrefix(pb.Host, "/home/t/.dabs/boxes/") || !strings.HasSuffix(pb.Host, "/sessions") {
		t.Errorf("perbox host = %q, want a per-box dir under ~/.dabs/boxes/…/sessions", pb.Host)
	}
	if pb.RO {
		t.Errorf("perbox mount is read-only; the box must be able to write it")
	}
	// The shared /cfg mount is untouched and precedes the overlay.
	if up.Mounts[0] != (sandbox.Mount{Host: "/home/t/vault", Path: "/cfg"}) {
		t.Errorf("first mount = %+v, want the shared /cfg mount intact", up.Mounts[0])
	}
	// The per-box dir is created on the host so the bind mount resolves.
	made := false
	for _, m := range fd.mkdirs {
		if m == pb.Host {
			made = true
		}
	}
	if !made {
		t.Errorf("per-box dir %q was not created on the host (mkdirs=%v)", pb.Host, fd.mkdirs)
	}
}

// CONTRACT: a `perbox:` source is visible in the look-before-run summary like
// every other source, so a user approves the mount that actually gets applied.
func TestPerboxSourceShownInConfirm(t *testing.T) {
	y := `recipes:
  r:
    image: img
    command: [run]
    sources:
      - mount: /home/t/vault
        path: /cfg
      - perbox: sessions
        path: /cfg/sessions
`
	fd := baseData()
	fd.exists["/home/t/vault"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	asked := ""
	confirm := func(prompt string) bool { asked = prompt; return true }
	// Appending a command triggers the look-before-run confirmation.
	if err := newReal(y, fd, drv).WithConfirm(confirm).
		Recipe(params.Recipe{Name: "r", Cmd: []string{"x"}}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if !strings.Contains(asked, "perbox") || !strings.Contains(asked, "sessions → /cfg/sessions") {
		t.Errorf("confirm summary omits the perbox source:\n%s", asked)
	}
}

// --- tests: appended command + confirmation + `dabs do` -----------------------

const appendRecipe = `recipes:
  m:
    image: img
    command: [run, it]
    sources:
      - mount: /d
        path: /work
`

// yes/no confirm stubs for the look-before-run gate.
func yes(string) bool { return true }
func no(string) bool  { return false }

// CONTRACT: a trailing command from `dabs recipe <name> <cmd…>` is APPENDED to
// the recipe's own command, and (once approved) that full argv is what runs.
func TestRecipeAppendsCommand(t *testing.T) {
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	asked := ""
	confirm := func(prompt string) bool { asked = prompt; return true }
	err := newReal(appendRecipe, fd, drv).WithConfirm(confirm).
		Recipe(params.Recipe{Name: "m", Cmd: []string{"--flag", "x"}})
	if err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "run it --flag x" {
		t.Errorf("run cmd = %v, want [run it --flag x]", drv.runs)
	}
	// The confirmation must have shown the recipe and the exact command.
	if !strings.Contains(asked, `recipe "m"`) || !strings.Contains(asked, "run it --flag x") {
		t.Errorf("confirmation prompt missing recipe/command: %q", asked)
	}
}

// CONTRACT: denying the confirmation aborts BEFORE any box is built or run.
func TestRecipeCommandDenyAborts(t *testing.T) {
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(appendRecipe, fd, drv).WithConfirm(no).
		Recipe(params.Recipe{Name: "m", Cmd: []string{"rm", "-rf", "/"}})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want aborted error, got %v", err)
	}
	if len(drv.ups) != 0 || len(drv.runs) != 0 {
		t.Errorf("box touched despite a denied confirmation: ups=%v runs=%v", drv.ups, drv.runs)
	}
}

// CONTRACT: running a recipe with NO appended command never prompts.
func TestRecipeNoCommandNoConfirm(t *testing.T) {
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	confirm := func(string) bool { t.Fatalf("must not prompt when no command is appended"); return false }
	if err := newReal(appendRecipe, fd, drv).WithConfirm(confirm).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "run it" {
		t.Errorf("run cmd = %v, want [run it]", drv.runs)
	}
}

// CONTRACT: `dabs do` runs the dabs.yaml default recipe with the command
// appended.
func TestDoUsesDefaultRecipe(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{"dabs.yaml": []byte(`default: dev
recipes:
  dev:
    image: img
    command: [devcmd]
    sources:
      - mount: /d
        path: /work
`)}
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal("", fd, drv).WithConfirm(yes).Do(params.Do{Cmd: []string{"ls", "-a"}}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "devcmd ls -a" {
		t.Errorf("run cmd = %v, want [devcmd ls -a]", drv.runs)
	}
}

// CONTRACT: with no default set, `dabs do` falls back to the `sh` recipe.
func TestDoFallsBackToShell(t *testing.T) {
	// A user recipes.yaml overrides the bundled `sh` with a simple, mountable
	// box (no cwd/git needed), and sets NO default — exercising the fallback.
	y := `recipes:
  sh:
    image: img
    command: [sh]
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).WithConfirm(yes).Do(params.Do{Cmd: []string{"-c", "echo hi"}}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "sh -c echo hi" {
		t.Errorf("run cmd = %v, want [sh -c echo hi]", drv.runs)
	}
}

// CONTRACT: the confirm summary shows INVALID sources too — a look-before-run
// that hides a malformed source isn't a trustworthy picture.
func TestConfirmSummaryShowsInvalidSource(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [run]
    sources:
      - path: /work
` // names none of mount/worktree/copy → Kind() errors
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	asked := ""
	confirm := func(prompt string) bool { asked = prompt; return true }
	// Appending a command triggers confirm; validation then rejects the source.
	err := newReal(y, fd, drv).WithConfirm(confirm).Recipe(params.Recipe{Name: "m", Cmd: []string{"x"}})
	if err == nil {
		t.Fatalf("want a validation error for the invalid source")
	}
	if !strings.Contains(asked, "invalid source") {
		t.Fatalf("confirm summary hid the invalid source: %q", asked)
	}
}

// CONTRACT: `dabs do` ALWAYS confirms first — even with NO appended command it
// must not launch a box unprompted, and a denial aborts before anything builds.
func TestDoConfirmsEvenWithoutCommand(t *testing.T) {
	y := `recipes:
  sh:
    image: img
    command: [sh]
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	asked := ""
	confirm := func(prompt string) bool { asked = prompt; return false }
	err := newReal(y, fd, drv).WithConfirm(confirm).Do(params.Do{}) // no command
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("`dabs do` must confirm and honor a denial; got err %v", err)
	}
	if asked == "" {
		t.Fatalf("`dabs do` launched without confirming (no prompt shown)")
	}
	if len(drv.ups) != 0 || len(drv.runs) != 0 {
		t.Errorf("box touched despite a denied `dabs do`: ups=%v runs=%v", drv.ups, drv.runs)
	}
}

// --- tests: recipe target routing (the last dabs.json field) ------------------

// CONTRACT: a recipe's `target` routes the box to that fleet driver (default
// local) — the one manifest field recipes were missing.
func TestRecipeTargetRoutesToDriver(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    target: remote
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	fd.files = map[string][]byte{fd.home + "/.dabs/recipes.yaml": []byte(y)}
	local := &fakeDriver{built: map[string]bool{"img": true}}
	remote := &fakeDriver{built: map[string]bool{"img": true}}
	r := actions.New(
		map[string]sandbox.Driver{"local": local, "remote": remote},
		[]string{"local", "remote"}, fstest.MapFS{}, fd,
	)
	if err := r.Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(remote.ups) != 1 || len(local.ups) != 0 {
		t.Fatalf("target=remote routed wrong: local ups=%d remote ups=%d", len(local.ups), len(remote.ups))
	}
}

// CONTRACT: an unknown target fails clearly (proving target flows to driverFor).
func TestRecipeUnknownTargetErrors(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    target: nope
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "no sandbox target") {
		t.Fatalf("want unknown-target error, got %v", err)
	}
}

// CONTRACT: keep:true leaves the box alive after the command (the user reaps it
// with `dabs down`); default deletes it. This is the "give me a box to work in"
// vs "run this query" distinction.
func TestRecipeKeepLeavesBoxAlive(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    keep: true
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.ups) != 1 {
		t.Fatalf("box not brought up: ups=%d", len(drv.ups))
	}
	if len(drv.downs) != 0 {
		t.Fatalf("keep:true still deleted the box: downs=%v", drv.downs)
	}
}

// `dabs recipes` prints each recipe's description on the SAME line as its name,
// and puts image= and cmd= on their own separate indented lines below.
func TestRecipesListsDescriptionOnNameLine(t *testing.T) {
	y := `recipes:
  m:
    description: a friendly clean box
    image: img
    command: [sh, -c, run]
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	out := captureStdout(t, func() {
		if err := newReal(y, fd, drv).Recipes(params.Recipes{}); err != nil {
			t.Fatalf("Recipes: %v", err)
		}
	})

	// The description must land on the same line as the name "m".
	var nameLine string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "m") && strings.Contains(ln, "a friendly clean box") {
			nameLine = ln
		}
	}
	if nameLine == "" {
		t.Fatalf("description not on the recipe name line; output:\n%s", out)
	}
	// image= and cmd= must be on their own separate lines.
	imgLine, cmdLine := false, false
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "image=img") && !strings.Contains(ln, "cmd=") {
			imgLine = true
		}
		if strings.Contains(ln, "cmd=sh -c run") && !strings.Contains(ln, "image=") {
			cmdLine = true
		}
	}
	if !imgLine || !cmdLine {
		t.Fatalf("image= and cmd= not on their own lines (image=%v cmd=%v); output:\n%s", imgLine, cmdLine, out)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wp
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, rp)
		done <- b.String()
	}()
	fn()
	wp.Close()
	os.Stdout = old
	return <-done
}
