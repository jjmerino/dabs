package actions_test

// Component tests for the recipe engine: the whole Real.Recipe orchestration is
// driven through its public API with the two seams faked — sandbox.Driver and
// data.Data. Assertions are written from the CONTRACT (what a recipe should
// cause), not by mirroring the implementation, so they can actually fail.

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

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
	toplevel  map[string]error  // GitToplevel: dir present with nil err => repo root is the dir
	noCommits map[string]bool   // GitHasCommits false for these tops
	worktrees []string          // recorded GitAddWorktree dests
	mkdirs    []string
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
		return nil, nil
	}
	return nil, fs.ErrNotExist
}
func (f *fakeData) MkdirAll(p string, _ fs.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeData) MkdirTemp(string, string) (string, error) { return "/tmp/x", nil }
func (f *fakeData) RemoveAll(string) error                   { return nil }
func (f *fakeData) Getenv(k string) string                   { return f.env[k] }
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
	return actions.New(drivers, []string{"local"}, fstest.MapFS{}, imgs, fd)
}

func baseData() *fakeData {
	return &fakeData{home: "/home/t", env: map[string]string{}, exists: map[string]bool{}, toplevel: map[string]error{}, noCommits: map[string]bool{}}
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
	if len(drv.execs) != 1 {
		t.Fatalf("want one copy Exec, got %v", drv.execs)
	}
	if s := strings.Join(drv.execs[0], " "); !strings.Contains(s, "/work") || !strings.Contains(s, "cp ") {
		t.Errorf("copy exec = %q, want a cp into /work", s)
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
