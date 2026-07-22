package actions_test

// Component tests for the recipe engine: the whole Real.Recipe orchestration is
// driven through its public API with the two seams faked — sandbox.Driver and
// data.Data. Assertions are written from the CONTRACT (what a recipe should
// cause), not by mirroring the implementation, so they can actually fail.

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/data"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
)

// Compile-time proof the fakes satisfy the real seams.
var (
	_ sandbox.Driver = (*fakeDriver)(nil)
	_ data.Data      = (*fakeData)(nil)
)

// --- fake driver: records every op, returns canned results -------------------

type fakeDriver struct {
	built     map[string]bool // name -> HasImage answer
	buildErr  error           // if non-nil, Build fails (simulates a driver with no builder)
	builds    []sandbox.BuildSpec
	ups       []sandbox.Spec
	upErr     error
	execs     [][]string
	execErr   error // if non-nil, Exec fails (simulates a box that cannot be entered)
	runs      [][]string
	runErr    error
	downs     []string
	nInst     int
	infos     []sandbox.Info // what Ls reports (for name resolution in Down)
	kind      string         // Kind() override; "" → "fake" (a local, non-server driver)
	lsCall    *bool          // if non-nil, set true when Ls is called (proves contact)
	lsCount   int            // how many times Ls was called (pins drivers-query batching)
	lsErrOnce error          // if non-nil, the FIRST Ls call fails with it (a transient outage)
	lsPanic   bool           // if true, Ls panics — proves it was never called when the test passes
}

func (d *fakeDriver) Build(s sandbox.BuildSpec) error {
	if d.buildErr != nil {
		return d.buildErr
	}
	d.builds = append(d.builds, s)
	// A real build leaves the image present, so a later HasImage sees it — the
	// property the reuse-vs-rebuild decision turns on.
	if d.built == nil {
		d.built = map[string]bool{}
	}
	d.built[s.Name] = true
	return nil
}
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
	if d.execErr != nil {
		return "bwrap: Can't chdir to /work: No such file or directory", d.execErr
	}
	return "", nil
}
func (d *fakeDriver) Down(inst string) error { d.downs = append(d.downs, inst); return nil }
func (d *fakeDriver) Ls() ([]sandbox.Info, error) {
	if d.lsPanic {
		panic("Ls called on a driver that must not be contacted")
	}
	if d.lsCall != nil {
		*d.lsCall = true
	}
	d.lsCount++
	if d.lsErrOnce != nil {
		e := d.lsErrOnce
		d.lsErrOnce = nil
		return nil, e
	}
	return d.infos, nil
}
func (d *fakeDriver) Kind() string {
	if d.kind != "" {
		return d.kind
	}
	return "fake"
}

// --- fake data: canned fs/env/git, records mutations -------------------------

type fakeData struct {
	home      string
	cwd       string // Getwd: what relative paths resolve against
	env       map[string]string
	files     map[string][]byte // ReadFile
	exists    map[string]bool   // Stat -> exists
	isDir     map[string]bool   // Stat -> IsDir (subset of exists)
	toplevel  map[string]error  // GitToplevel: dir present with nil err => repo root is the dir
	noCommits map[string]bool   // GitHasCommits false for these tops
	worktrees []string          // recorded GitAddWorktree dests
	mkdirs    []string
	made      []string                  // exclusive Mkdir creations
	dirs      map[string][]string       // ReadDir results
	states    map[string]wtState        // GitState by worktree path
	removed   []string                  // recorded GitRemoveWorktree
	rmAll     []string                  // recorded RemoveAll
	copies    []string                  // recorded CopyDir
	commondir map[string]string         // GitCommonDir: worktree path -> parent .git (present => a worktree)
	foreign   map[string][]string       // GitListWorktrees: repo top -> linked worktree paths; an absent top errors (no git / not a repo)
	symlinks  map[string]string         // EvalSymlinks: path -> canonical form; an absent path errors, like an unresolvable one
	prompts   map[string]data.GitPrompt // GitPromptStatus: dir -> prompt state; an absent dir errors (not a git repo)
}

type wtState struct {
	branch string
	dirty  bool
	ahead  int
	landed bool // commits ahead whose content is already in the base (a squash merge)
}

func (f *fakeData) HomeDir() (string, error) { return f.home, nil }
func (f *fakeData) ReadFile(p string) ([]byte, error) {
	if b, ok := f.files[p]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}
func (f *fakeData) WriteFile(p string, b []byte, _ fs.FileMode) error {
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[p] = append([]byte(nil), b...)
	// A written node record makes its node listable, as it does on a real disk —
	// otherwise the fake would let a node be written and never found again.
	if strings.HasSuffix(p, "/"+nodeFileName) {
		dir := filepath.Dir(p)
		root := filepath.Dir(dir)
		if f.dirs == nil {
			f.dirs = map[string][]string{}
		}
		name := filepath.Base(dir)
		for _, have := range f.dirs[root] {
			if have == name {
				return nil
			}
		}
		f.dirs[root] = append(f.dirs[root], name)
	}
	return nil
}

// nodeFileName is the record actions writes for every node. The fake mirrors the
// real layout so a node written can be listed.
const nodeFileName = "dabs-node.json"

func (f *fakeData) AppendFile(p string, b []byte, _ fs.FileMode) error {
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[p] = append(f.files[p], b...)
	return nil
}
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

// Mkdir mirrors the exclusive create: a second Mkdir of the same path fails
// with fs.ErrExist, as the OS does.
func (f *fakeData) Mkdir(p string, _ fs.FileMode) error {
	for _, have := range f.made {
		if have == p {
			return fs.ErrExist
		}
	}
	f.made = append(f.made, p)
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeData) MkdirTemp(string, string) (string, error) { return "/tmp/x", nil }
func (f *fakeData) Getwd() (string, error)                   { return f.cwd, nil }
func (f *fakeData) CopyDir(src, dst string) error {
	f.copies = append(f.copies, src+" -> "+dst)
	return nil
}
func (f *fakeData) RemoveAll(p string) error {
	f.rmAll = append(f.rmAll, p)
	// Mirror the OS: a removed tree is GONE — the dir (so a later exclusive
	// Mkdir succeeds), and everything filed beneath it.
	kept := f.made[:0]
	for _, have := range f.made {
		if have != p && !strings.HasPrefix(have, p+"/") {
			kept = append(kept, have)
		}
	}
	f.made = kept
	for k := range f.files {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(f.files, k)
		}
	}
	for k := range f.dirs {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(f.dirs, k)
		}
	}
	for k := range f.exists {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(f.exists, k)
			delete(f.isDir, k)
		}
	}
	for k := range f.states {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(f.states, k)
		}
	}
	return nil
}
func (f *fakeData) Getenv(k string) string            { return f.env[k] }
func (f *fakeData) LookupEnv(k string) (string, bool) { v, ok := f.env[k]; return v, ok }
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
func (f *fakeData) ReadDir(dir string) ([]string, error) {
	// A path registered as a file cannot be listed — the OS errors with ENOTDIR.
	if _, ok := f.files[dir]; ok {
		return nil, errors.New("not a directory")
	}
	return f.dirs[dir], nil
}
func (f *fakeData) GitState(wt string) (string, bool, int, error) {
	// Mirror git: asking about a checkout with no state errors (exit 128 on a
	// missing path), it does not answer clean.
	s, ok := f.states[wt]
	if !ok {
		return "", false, 0, errors.New("git: exit status 128")
	}
	return s.branch, s.dirty, s.ahead, nil
}
func (f *fakeData) GitDiff(wt string) (string, error) { return "diff of " + wt, nil }
func (f *fakeData) GitLanded(wt string) (bool, error) {
	st, ok := f.states[wt]
	return ok && st.landed, nil
}
func (f *fakeData) GitRemoveWorktree(wt string) error {
	f.removed = append(f.removed, wt)
	return nil
}
func (f *fakeData) EvalSymlinks(p string) (string, error) {
	if c, ok := f.symlinks[p]; ok {
		return c, nil
	}
	return "", errors.New("lstat: no such file or directory")
}
func (f *fakeData) GitListWorktrees(top string) ([]string, error) {
	if wts, ok := f.foreign[top]; ok {
		return wts, nil
	}
	return nil, errors.New("git worktree list: exec: \"git\": executable file not found in $PATH")
}
func (f *fakeData) GitPromptStatus(dir string) (data.GitPrompt, error) {
	if p, ok := f.prompts[dir]; ok {
		return p, nil
	}
	return data.GitPrompt{}, errors.New("not a git repository")
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
	return &fakeData{home: "/home/t", cwd: "/cwd", env: map[string]string{}, exists: map[string]bool{}, isDir: map[string]bool{}, toplevel: map[string]error{}, noCommits: map[string]bool{}, states: map[string]wtState{}}
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
	fd.toplevel["/cwd"] = nil // the cwd is a repo whose root is the cwd
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

func TestRecipeCopySnapshotsOntoTheHostAndMountsIt(t *testing.T) {
	y := `recipes:
  c:
    image: img
    command: [x]
    sources:
      - copy: .
        path: /work
`
	fd := baseData()
	fd.exists["/cwd"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "c"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	// CONTRACT: a copy is a snapshot dabs OWNS — it lands in the new place's own
	// space on the HOST, so `down` can ask before reaping it and a human can read
	// what the box did. The box gets that directory as a live bind, not a copy of
	// a copy made inside it.
	up := onlyUp(t, drv)
	var work sandbox.Mount
	for _, m := range up.Mounts {
		if m.Path == "/work" {
			work = m
		}
	}
	if work.Host == "" {
		t.Fatalf("no mount at /work: %+v", up.Mounts)
	}
	if !strings.HasPrefix(work.Host, "/home/t/.dabs/nodes/") || !strings.Contains(work.Host, "/held/") {
		t.Errorf("copy mounted %q; want the place's own held space", work.Host)
	}
	// The snapshot was taken on the host, not by exec'ing cp inside the box.
	if len(fd.copies) != 1 || !strings.HasPrefix(fd.copies[0], "/cwd -> ") {
		t.Errorf("host copy = %v, want one copy of the cwd into the node", fd.copies)
	}
	if len(drv.execs) != 0 {
		t.Errorf("copy ran commands inside the box: %v", drv.execs)
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

// CONTRACT: two sources landing at the SAME box path are rejected — one would
// silently mask the other. Nesting at DIFFERENT paths stays legal.
func TestRecipeDuplicateBoxPathRejected(t *testing.T) {
	y := `recipes:
  dup:
    image: img
    command: [x]
    sources:
      - mount: /a
        path: /work
      - mkmount: /b
        path: /work
`
	fd := baseData()
	fd.exists["/a"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "dup"})
	if err == nil || !strings.Contains(err.Error(), "same box path") {
		t.Fatalf("want duplicate-box-path error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("invalid recipe still brought a box up: %v", drv.ups)
	}

	// Nested but DISTINCT box paths still pass.
	ok := `recipes:
  nest:
    image: img
    command: [x]
    sources:
      - mount: /a
        path: /work
      - mkmount: /b
        path: /work/sub
`
	fd2 := baseData()
	fd2.exists["/a"] = true
	drv2 := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(ok, fd2, drv2).Recipe(params.Recipe{Name: "nest"}); err != nil {
		t.Fatalf("nested distinct paths must pass, got %v", err)
	}
}

// CONTRACT: a Dockerfile-backed recipe whose image is built AND whose Dockerfile
// is UNCHANGED is not rebuilt on `dabs recipe` — the box boots the existing
// image (the #39 speedup). The first boot builds and records the source; the
// second, with nothing edited, reuses it.
func TestRecipeDockerfileImageAlreadyBuiltDoesNotBuild(t *testing.T) {
	y := `recipes:
  d:
    image: { dockerfile: Dockerfile, context: . }
    command: [run, it]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{"/cwd/Dockerfile": []byte("FROM alpine\n")}
	drv := &fakeDriver{}
	r := newReal(y, fd, drv)
	if err := r.Recipe(params.Recipe{Name: "d"}); err != nil { // builds once, records
		t.Fatalf("Recipe: %v", err)
	}
	if err := r.Recipe(params.Recipe{Name: "d"}); err != nil { // unchanged → reuse
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.builds) != 1 {
		t.Errorf("unchanged Dockerfile — want a single Build, got %+v", drv.builds)
	}
	if drv.ups[len(drv.ups)-1].Name != "d" {
		t.Fatalf("want the box booted from image d, got ups=%+v", drv.ups)
	}
}

// CONTRACT: a Dockerfile-backed recipe whose image is MISSING is built once, on
// its own name, before the box boots.
func TestRecipeDockerfileImageMissingBuildsOnce(t *testing.T) {
	y := `recipes:
  d:
    image: { dockerfile: Dockerfile, context: . }
    command: [run, it]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{"/cwd/Dockerfile": []byte("FROM alpine\n")}
	drv := &fakeDriver{built: map[string]bool{"d": false}} // not built yet
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "d"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.builds) != 1 || drv.builds[0].Name != "d" {
		t.Fatalf("image missing — want one Build of d, got %+v", drv.builds)
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
	fd.toplevel["/cwd"] = nil                                // "." IS a repo...
	fd.noCommits["/cwd"] = true                              // ...but has no commits
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
	// No name → the default-recipe path, which ALWAYS confirms before running.
	if err := newReal("", fd, drv).WithConfirm(yes).Recipe(params.Recipe{}); err != nil {
		t.Fatalf("default recipe: %v", err)
	}
	if len(drv.runs) != 1 || drv.runs[0][0] != "devcmd" {
		t.Errorf("default not run: %v", drv.runs)
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

// --- tests: recipe --worktree (bind a recipe onto an existing worktree) -------

// CONTRACT: `dabs recipe <recipe> --worktree <wt>` on a `worktree: .` recipe
// ATTACHES the existing worktree (mounts it live, never forks a new branch) and
// also mounts the parent .git so git resolves in-box. Non-`.` sources pass through.
func TestWorktreeFlagAttachesWorktreeAndGitDir(t *testing.T) {
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
	wt := seedWorktreeNode(fd, "wt1", wtState{branch: "dabs/wt1"})
	fd.exists["/home/t/vault"] = true
	fd.exists["/repo/.git"] = true // parent store exists (git rev-parse yields a real path)
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Args: []string{"w"}, Worktree: "wt1"}); err != nil {
		t.Fatalf("recipe --worktree: %v", err)
	}
	// It must NOT fork a fresh worktree.
	if len(fd.worktrees) != 0 {
		t.Fatalf("--worktree forked a worktree, want none: %v", fd.worktrees)
	}
	up := onlyUp(t, drv)
	want := []sandbox.Mount{
		{Host: "/home/t/vault", Path: "/root/.cfg"}, // passthrough
		{Host: wt, Path: "/work"},                   // the worktree, attached live
		{Host: "/repo/.git", Path: "/repo/.git"},    // parent store, so git works in-box
	}
	// The SET of mounts is the contract; their order is not — actions order mounts
	// parent-before-child, which is a separate contract with its own test.
	if len(up.Mounts) != len(want) {
		t.Fatalf("mounts = %+v, want %+v", up.Mounts, want)
	}
	for _, w := range want {
		found := false
		for _, got := range up.Mounts {
			if got == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing mount %+v; got %+v", w, up.Mounts)
		}
	}
}

// CONTRACT: `--worktree` on a recipe that has no `.` source is a user error, not
// a silent no-op — there's nothing to bind the worktree to.
func TestWorktreeFlagRecipeWithoutDotSourceErrors(t *testing.T) {
	y := `recipes:
  v:
    image: img
    command: [x]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	seedWorktreeNode(fd, "wt1", wtState{branch: "dabs/wt1"})
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Args: []string{"v"}, Worktree: "wt1"})
	if err == nil || !strings.Contains(err.Error(), "no `.` source") {
		t.Fatalf("want 'no `.` source' error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite bad --worktree: %v", drv.ups)
	}
}

// CONTRACT: `--worktree` onto a missing worktree fails cleanly before any box work.
func TestWorktreeFlagMissingWorktreeErrors(t *testing.T) {
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
	err := newReal(y, fd, drv).Recipe(params.Recipe{Args: []string{"w"}, Worktree: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("want 'no worktree' error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite missing worktree: %v", drv.ups)
	}
}

// --- tests: the generic perbox source -----------------------------------------

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
		Recipe(params.Recipe{Args: []string{"m", "--flag", "x"}})
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
		Recipe(params.Recipe{Args: []string{"m", "rm", "-rf", "/"}})
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

// CONTRACT: `dabs recipe -- <cmd…>` runs the dabs.yaml default recipe with the
// command after `--` appended (the replacement for the old `dabs do`).
func TestRecipeDefaultAppendsCommand(t *testing.T) {
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
	if err := newReal("", fd, drv).WithConfirm(yes).Recipe(params.Recipe{Args: []string{"ls", "-a"}, Default: true}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "devcmd ls -a" {
		t.Errorf("run cmd = %v, want [devcmd ls -a]", drv.runs)
	}
}

// CONTRACT: with no default set, the default-recipe path falls back to `sh`.
func TestRecipeDefaultFallsBackToShell(t *testing.T) {
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
	if err := newReal(y, fd, drv).WithConfirm(yes).Recipe(params.Recipe{Args: []string{"-c", "echo hi"}, Default: true}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "sh -c echo hi" {
		t.Errorf("run cmd = %v, want [sh -c echo hi]", drv.runs)
	}
}

// CONTRACT (grammar case 3): a leading `--` forces the default recipe even when
// the next token names a recipe — so `dabs recipe -- sh …` appends `sh` to the
// default's command instead of selecting the `sh` recipe.
func TestRecipeDashDashForcesDefault(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{"dabs.yaml": []byte(`default: dev
recipes:
  dev:
    image: img
    command: [devcmd]
    sources:
      - mount: /d
        path: /work
  sh:
    image: img
    command: [sh]
    sources:
      - mount: /d
        path: /work
`)}
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal("", fd, drv).WithConfirm(yes).Recipe(params.Recipe{Args: []string{"sh", "-c", "echo hi"}, Default: true}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "devcmd sh -c echo hi" {
		t.Errorf("run cmd = %v, want [devcmd sh -c echo hi]", drv.runs)
	}
}

// CONTRACT (grammar case 1): a first token that IS a known recipe selects it and
// appends only the rest — and without `--`, that recipe wins over the default.
func TestRecipeKnownNameSelectsIt(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{"dabs.yaml": []byte(`default: dev
recipes:
  dev:
    image: img
    command: [devcmd]
    sources:
      - mount: /d
        path: /work
  sh:
    image: img
    command: [sh]
    sources:
      - mount: /d
        path: /work
`)}
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal("", fd, drv).WithConfirm(yes).Recipe(params.Recipe{Args: []string{"sh", "-c", "echo hi"}}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if len(drv.runs) != 1 || strings.Join(drv.runs[0], " ") != "sh -c echo hi" {
		t.Errorf("run cmd = %v, want [sh -c echo hi]", drv.runs)
	}
}

// CONTRACT: a bare first token that is neither `--` nor a known recipe is an
// ERROR listing the known recipes — a typo must never silently become a command
// on the default recipe. The error hints at the `-- <cmd>` form and touches no box.
func TestRecipeUnknownFirstTokenErrors(t *testing.T) {
	y := `recipes:
  known:
    image: img
    command: [run]
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).WithConfirm(yes).Recipe(params.Recipe{Args: []string{"nope", "-x"}})
	if err == nil {
		t.Fatalf("want an error for an unknown first token, got nil")
	}
	if !strings.Contains(err.Error(), `no recipe "nope"`) || !strings.Contains(err.Error(), "known:") {
		t.Errorf("error missing unknown-name/known-list: %v", err)
	}
	if !strings.Contains(err.Error(), "dabs recipe -- nope") {
		t.Errorf("error missing the `-- <cmd>` hint: %v", err)
	}
	if len(drv.ups) != 0 || len(drv.runs) != 0 {
		t.Errorf("box touched despite an unknown-token error: ups=%v runs=%v", drv.ups, drv.runs)
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
	err := newReal(y, fd, drv).WithConfirm(confirm).Recipe(params.Recipe{Args: []string{"m", "x"}})
	if err == nil {
		t.Fatalf("want a validation error for the invalid source")
	}
	if !strings.Contains(asked, "invalid source") {
		t.Fatalf("confirm summary hid the invalid source: %q", asked)
	}
}

// CONTRACT: the default-recipe path ALWAYS confirms first — even with NO
// appended command it must not launch a box unprompted, and a denial aborts
// before anything builds.
func TestRecipeDefaultConfirmsEvenWithoutCommand(t *testing.T) {
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
	err := newReal(y, fd, drv).WithConfirm(confirm).Recipe(params.Recipe{}) // no args → default path
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("the default-recipe path must confirm and honor a denial; got err %v", err)
	}
	if asked == "" {
		t.Fatalf("the default recipe launched without confirming (no prompt shown)")
	}
	if len(drv.ups) != 0 || len(drv.runs) != 0 {
		t.Errorf("box touched despite a denied run: ups=%v runs=%v", drv.ups, drv.runs)
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
// with `dabs rm --keep`); default deletes it. This is the "give me a box to work in"
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

// `dabs recipes` renders one line per recipe: the name then its description,
// and nothing else — no image=, cmd=, or source lines.
func TestRecipesListsNameAndDescription(t *testing.T) {
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
		t.Fatalf("name and description not on one line; output:\n%s", out)
	}
	// No image=, cmd=, or source arrow lines survive.
	if strings.Contains(out, "image=") || strings.Contains(out, "cmd=") || strings.Contains(out, "→") {
		t.Fatalf("recipes output still carries image=/cmd=/source detail; output:\n%s", out)
	}
	// One line per recipe — a count, not just no-blanks, so a reintroduced
	// per-recipe detail line fails even without an image=/cmd= marker. The
	// registry is the bundled recipes plus the one project recipe above.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	reg, err := recipe.Parse(recipe.Bundled)
	if err != nil {
		t.Fatal(err)
	}
	want := len(reg.Names()) + 1
	if len(lines) != want {
		t.Fatalf("recipes printed %d lines for %d recipes:\n%s", len(lines), want, out)
	}
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			t.Fatalf("blank line in recipes output:\n%s", out)
		}
	}
}

// --- tests: source/path sanity (bug hunt) -------------------------------------

// CONTRACT: a $NODE_*/$PARENT_* token belongs in a source ORIGIN, not a box
// PATH. An unexpanded variable in a box path would make a directory literally
// named "$NODE_VOLUME" at box root, so it is rejected up front.
func TestRecipeVarInBoxPathIsRejected(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: $NODE_VOLUME/x
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "variable") {
		t.Fatalf("want a box-path variable error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite a variable box path: %v", drv.ups)
	}
}

// CONTRACT: a non-absolute box path is rejected — a relative path would be
// silently rooted at / and leave the workdir empty.
func TestRecipeRelativeBoxPathIsRejected(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: relative
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("want a non-absolute box-path error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite a relative box path: %v", drv.ups)
	}
}

// CONTRACT: a box path with `..` is rejected — it escapes the declared workdir
// (path: /work/../etc/injected would mount at /etc/injected).
func TestRecipeDotDotBoxPathIsRejected(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: /work/../etc/injected
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("want a `..` box-path error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite an escaping box path: %v", drv.ups)
	}
}

// CONTRACT: $NODE_ID in a box path expands to the box's own id, so a mount can
// auto-namespace its in-box destination per box (path: /$NODE_ID → /<id>). With
// --name the id is deterministic, so the mount lands at exactly that name.
func TestRecipeNodeIDExpandsInBoxPath(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: /$NODE_ID
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	up := onlyUp(t, drv)
	if len(up.Mounts) != 1 || up.Mounts[0] != (sandbox.Mount{Host: "/d", Path: "/mybox"}) {
		t.Errorf("Up mounts = %+v, want one {/d -> /mybox}", up.Mounts)
	}
}

// CONTRACT: without --name the id is minted (<recipe>-<hex>), and $NODE_ID
// resolves to THAT id — never left as the literal token, which would make a box
// directory named "$NODE_ID".
func TestRecipeNodeIDExpandsToMintedID(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: /$NODE_ID
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	got := onlyUp(t, drv).Mounts[0].Path
	if !strings.HasPrefix(got, "/m-") || strings.Contains(got, "$") {
		t.Errorf("box path = %q, want /m-<hex> (minted id), never the literal token", got)
	}
}

// CONTRACT: $NODE_ID is a convenience, not an escape hatch — a `..` sitting
// beside it in the box path is still caught, so /$NODE_ID/../../etc cannot
// smuggle a mount out of the box's workdir. (Node ids themselves are slugs, so
// the id can never contribute a `..` of its own.)
func TestRecipeNodeIDCannotSmuggleTraversal(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: /$NODE_ID/../../etc/injected
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"})
	if err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("want a `..` box-path error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite an escaping box path: %v", drv.ups)
	}
}

// CONTRACT: only $NODE_ID resolves in a box path — a space var mixed in beside it
// is still rejected, so $NODE_ID does not open the door to the others.
func TestRecipeNodeIDDoesNotAdmitOtherVars(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: /$NODE_ID/$NODE_VOLUME
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"})
	if err == nil || !strings.Contains(err.Error(), "variable") {
		t.Fatalf("want a box-path variable error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite a space var in the box path: %v", drv.ups)
	}
}

// CONTRACT: $NODE_ID in the recipe workdir expands to the box's own id, so the
// box's cwd can auto-namespace per box (workdir: /$NODE_ID → /<id>) — the same
// contract mount paths get, taken raw would leave a literal /$NODE_ID cwd.
func TestRecipeNodeIDExpandsInWorkdir(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    workdir: /$NODE_ID
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if got := onlyUp(t, drv).Workdir; got != "/mybox" {
		t.Errorf("Up workdir = %q, want /mybox", got)
	}
}

// CONTRACT: an unset workdir stays the /work default — expandBoxPath leaves a
// $-free (here empty) workdir untouched, so adding $NODE_ID expansion does not
// disturb the no-workdir case.
func TestRecipeEmptyWorkdirKeepsDefault(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	if got := onlyUp(t, drv).Workdir; got != "/work" {
		t.Errorf("Up workdir = %q, want /work default", got)
	}
}

// CONTRACT: only $NODE_ID resolves in the workdir — a space var mixed in is
// rejected exactly as it is in a box path, so the workdir is not a back door to
// the other vars.
func TestRecipeNodeIDWorkdirDoesNotAdmitOtherVars(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    workdir: /$NODE_VOLUME
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m", NodeName: "mybox"})
	if err == nil || !strings.Contains(err.Error(), "variable") {
		t.Fatalf("want a box-path variable error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite a space var in the workdir: %v", drv.ups)
	}
}

// CONTRACT: a RELATIVE source origin that climbs above the project with `..` is
// rejected — dabs cannot track or reap a place outside its namespace. Absolute
// origins remain an explicit user choice and are left alone.
func TestRecipeEscapingRelativeOriginIsRejected(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mkmount: ../escape
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "escapes the project") {
		t.Fatalf("want an origin-escape error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite an escaping origin: %v", drv.ups)
	}
}

// CONTRACT: an absolute source origin is NOT rejected as an escape — the shipped
// claude recipe uses `mkmount: ~/.dabs/shared/claude`, an explicit choice.
func TestRecipeAbsoluteOriginIsAllowed(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mkmount: /opt/x
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("absolute origin should be allowed: %v", err)
	}
	if len(drv.ups) != 1 {
		t.Errorf("want the box up with an absolute mkmount origin: %v", drv.ups)
	}
}

// CONTRACT: a source path built from a dabs space var ($NODE_VOLUME) may not use
// `..` to climb out of the space it names — that would provision a directory
// outside the dabs-managed node tree.
func TestRecipeSpaceVarCannotEscapeItsSpace(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mkmount: $NODE_VOLUME/../../../../../../etc/dabs-escape
        path: /work
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "escapes its $NODE_VOLUME space") {
		t.Fatalf("want a space-escape error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite an escaping space path: %v", drv.ups)
	}
}

// CONTRACT: a `..` that stays inside the space is fine, and a legitimate nested
// path resolves within the space and the box comes up.
func TestRecipeSpaceVarNestedPathResolvesInside(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mkmount: $NODE_VOLUME/ok/sub
        path: /cache
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("a nested space path should resolve inside: %v", err)
	}
	if len(drv.ups) != 1 {
		t.Errorf("want the box up with a nested space path: %v", drv.ups)
	}
}

// CONTRACT: a recipe with more than one `.` source is rejected — each `.` cuts a
// chain tip, but a single box can only stand on one place.
func TestRecipeMultipleDotSourcesRejected(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
      - copy: .
        path: /snap
`
	fd := baseData()
	fd.toplevel["/cwd"] = nil
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "one `.` source") {
		t.Fatalf("want a multiple-`.`-source error, got %v", err)
	}
	if len(drv.ups) != 0 || len(fd.worktrees) != 0 {
		t.Errorf("side effects despite two `.` sources: ups=%v worktrees=%v", drv.ups, fd.worktrees)
	}
}

// CONTRACT: a `copy:` whose `at:` lands INSIDE the copy source is rejected —
// cp would recurse into itself (dest/dest/…) and fill the disk.
func TestRecipeCopyAtInsideSourceRejected(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - copy: .
        at: /cwd/inner
        path: /work
`
	fd := baseData()
	fd.exists["/cwd"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "recurse into itself") {
		t.Fatalf("want a copy-into-itself error, got %v", err)
	}
	if len(fd.copies) != 0 {
		t.Errorf("copy ran despite the self-recursive destination: %v", fd.copies)
	}
}

// CONTRACT: a recipe with an empty command that gets appended argv is rejected —
// the argv would reach the driver as bare options (bwrap: Unknown option -c).
func TestRecipeEmptyCommandWithAppendedArgvRejected(t *testing.T) {
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
	err := newReal(y, fd, drv).WithConfirm(yes).Recipe(params.Recipe{Args: []string{"x", "-c", "echo hi"}})
	if err == nil || !strings.Contains(err.Error(), "no command") {
		t.Fatalf("want a no-command error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up feeding argv to no command: %v", drv.ups)
	}
}

// CONTRACT: `dabs recipe --detach` on a boxless (imageless) recipe provisions the
// place(s) and stops — the SAME outcome as a plain `dabs recipe`, not a spurious
// "has no path" error.
func TestUpOnBoxlessRecipeProvisionsLikeRecipe(t *testing.T) {
	// A boxless recipe must have a source that MAKES a place: copy or worktree.
	// A live mount makes none (the box, which there is not, would be the thing
	// that mounts it), so a copy source is what a boxless recipe uses.
	y := `recipes:
  place:
    sources:
      - copy: /data
`
	run := func(do func(actions.Real) error) *fakeDriver {
		fd := baseData()
		fd.exists["/data"] = true
		drv := &fakeDriver{built: map[string]bool{"img": true}}
		if err := do(newReal(y, fd, drv)); err != nil {
			t.Fatalf("boxless: %v", err)
		}
		if len(drv.ups) != 0 {
			t.Errorf("boxless recipe brought a box up: %v", drv.ups)
		}
		return drv
	}
	run(func(r actions.Real) error { return r.Recipe(params.Recipe{Name: "place"}) })
	run(func(r actions.Real) error { return r.Recipe(params.Recipe{Detach: true, Args: []string{"place"}}) })
}

// CONTRACT: running dabs from inside its OWN storage (~/.dabs/...) is refused —
// it would mark the node store itself as a project.
func TestProvisioningInsideDabsStoreRefused(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.cwd = "/home/t/.dabs/nodes/foo-1234"
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "dabs's own storage") {
		t.Fatalf("want a refuse-inside-storage error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up from inside dabs storage: %v", drv.ups)
	}
}

// CONTRACT: setting PATH in a recipe's env WARNS (to stderr) that it replaces the
// image PATH — the box still comes up, but the caller is told commands may not
// resolve.
func TestRecipePathInEnvWarns(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    env: { PATH: /only/here }
    sources:
      - mount: /d
        path: /work
`
	fd := baseData()
	fd.exists["/d"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	errc := captureStderr(t, func() {
		if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
			t.Fatalf("Recipe: %v", err)
		}
	})
	if !strings.Contains(errc, "PATH") || !strings.Contains(errc, "REPLACES") {
		t.Errorf("want a PATH-replacement warning on stderr, got %q", errc)
	}
	if len(drv.ups) != 1 {
		t.Errorf("the box should still come up: %v", drv.ups)
	}
}

// --- tests: --worktree prefix resolution --------------------------------------

// CONTRACT: `dabs recipe <recipe> --worktree <wt>` resolves the worktree by
// unambiguous PREFIX, git-style — a unique prefix binds the full worktree.
func TestWorktreeFlagResolvesWorktreeByPrefix(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData()
	seedWorktreeNode(fd, "repo-c1d2e3f4", wtState{branch: "dabs/c1d2e3f4"})
	fd.exists["/repo/.git"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Args: []string{"w"}, Worktree: "repo-c1"}); err != nil {
		t.Fatalf("--worktree by prefix: %v", err)
	}
	if len(fd.worktrees) != 0 {
		t.Fatalf("--worktree forked a worktree, want none: %v", fd.worktrees)
	}
	if len(drv.ups) != 1 {
		t.Fatalf("--worktree did not bring the box up: %v", drv.ups)
	}
}

// CONTRACT: an AMBIGUOUS worktree prefix reports "ambiguous" and lists matches —
// not a bare "no worktree".
func TestWorktreeFlagAmbiguousWorktreePrefixErrors(t *testing.T) {
	y := `recipes:
  w:
    image: img
    command: [x]
    sources:
      - worktree: .
        path: /work
`
	fd := baseData()
	seedWorktreeNode(fd, "repo-aaaa1111", wtState{branch: "dabs/aaaa1111"})
	seedWorktreeNode(fd, "repo-aaaa2222", wtState{branch: "dabs/aaaa2222"})
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Args: []string{"w"}, Worktree: "repo-aaaa"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want an ambiguous-prefix error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up despite an ambiguous --worktree: %v", drv.ups)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = wp
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, rp)
		done <- b.String()
	}()
	fn()
	wp.Close()
	os.Stderr = old
	return <-done
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

// CONTRACT: a RELATIVE source origin reaches the driver ABSOLUTE (anchored on
// the cwd). A driver only ever takes exact paths — docker rejects a relative
// bind ("mount path must be absolute"), so resolution must happen in actions.
func TestRelativeSourceOriginReachesDriverAbsolute(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: .
        path: /work
      - copy: stage
        path: /tmp/s
`
	const cwd = "/work/proj" // the fake's cwd: relative origins anchor here
	fd := baseData()
	fd.cwd = cwd
	fd.exists[cwd] = true
	fd.exists[filepath.Join(cwd, "stage")] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	up := onlyUp(t, drv)
	for _, m := range up.Mounts {
		if !filepath.IsAbs(m.Host) {
			t.Errorf("driver got relative mount host %q; want absolute", m.Host)
		}
	}
	if len(up.Mounts) != 2 || up.Mounts[0].Host != cwd {
		t.Errorf("Up mounts = %+v, want `.` mounted as the cwd %s", up.Mounts, cwd)
	}
	// The copy source is staged read-only, then copied in-box.
	if up.Mounts[1].Host != filepath.Join(cwd, "stage") {
		t.Errorf("copy source host = %q, want %q", up.Mounts[1].Host, filepath.Join(cwd, "stage"))
	}
}

// CONTRACT: a dabs.yaml loaded BY PATH anchors its relative source origins on
// its OWN directory (as it already does for its image dockerfile/context), so
// `dabs recipe path/to/box --detach` provisions the same box from any cwd.
func TestUpFromDabsYamlPathRebasesSourcePaths(t *testing.T) {
	y := `default: base
recipes:
  base:
    image: img
    command: [x]
    sources:
      - copy: assets
        path: /tmp/stage
`
	fd := baseData()
	path := "/proj/box/dabs.yaml"
	fd.exists[path] = true
	fd.exists["/proj/box/assets"] = true
	fd.files = map[string][]byte{path: []byte(y)}
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal("", fd, drv).Recipe(params.Recipe{Detach: true, Args: []string{path}}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	up := onlyUp(t, drv)
	if len(up.Mounts) != 1 || up.Mounts[0].Host != "/proj/box/assets" {
		t.Errorf("Up mounts = %+v, want the source anchored on the dabs.yaml dir (/proj/box/assets)", up.Mounts)
	}
}

// CONTRACT: `--detach` smoke-checks the box by entering it once. If that enter
// fails (a source over `/`, a missing `workdir:`, a masked child mount — all
// surface as a driver error), the boot did not really succeed: no success block
// prints, the box is reaped, and the error carries the driver's message.
func TestUpReapsAndErrorsWhenBoxCannotBeEntered(t *testing.T) {
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
	drv := &fakeDriver{built: map[string]bool{"img": true}, execErr: errors.New("bwrap: Can't chdir to /work: No such file or directory")}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true, Args: []string{"m"}})
	if err == nil {
		t.Fatal("want an error when the smoke check fails, got nil")
	}
	if !strings.Contains(err.Error(), "chdir") {
		t.Errorf("error = %v, want it to surface the driver message", err)
	}
	if len(drv.downs) != 1 {
		t.Errorf("box not reaped: downs = %v, want exactly one", drv.downs)
	}
}

// CONTRACT: a healthy `--detach` box passes the smoke check, prints its id, and
// is NOT reaped — the box stays up for the user to reach in and eventually `rm`.
func TestUpKeepsBoxAndPrintsIDWhenSmokeCheckPasses(t *testing.T) {
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
	var out string
	err := errors.New("unset")
	out = captureStdout(t, func() {
		err = newReal(y, fd, drv).Recipe(params.Recipe{Detach: true, Args: []string{"m"}})
	})
	if err != nil {
		t.Fatalf("Recipe --detach: %v", err)
	}
	if len(drv.execs) != 1 || strings.Join(drv.execs[0], " ") != "true" {
		t.Errorf("smoke check = %v, want one exec of [true]", drv.execs)
	}
	if len(drv.downs) != 0 {
		t.Errorf("box was reaped on a good boot: downs = %v", drv.downs)
	}
	if !strings.Contains(out, "id:") || !strings.Contains(out, "img-inst") {
		t.Errorf("stdout = %q, want the success block with the instance id", out)
	}
}

// CONTRACT: a mount NESTED inside another reaches the driver AFTER its parent,
// however the recipe declared them. bwrap binds in argv order, so a parent
// listed after its child masks the child — the box gets the parent's own content
// at the child's path, with no error. Apple/docker resolve nesting themselves, so
// a recipe authored there would break only on Linux. Actions decide the order.
func TestNestedMountsReachDriverParentFirst(t *testing.T) {
	// Declared deepest-first, and out of order, on purpose.
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: /h/sessions
        path: /root/.claude/projects/inner
      - mount: /h/work
        path: /work
      - mount: /h/claude
        path: /root/.claude
      - mount: /h/proj
        path: /root/.claude/projects
`
	fd := baseData()
	for _, p := range []string{"/h/sessions", "/h/work", "/h/claude", "/h/proj"} {
		fd.exists[p] = true
	}
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	got := onlyUp(t, drv).Mounts
	seen := map[string]int{}
	for i, m := range got {
		for parent, pi := range seen {
			if strings.HasPrefix(m.Path, parent+"/") && pi > i {
				t.Errorf("mount %s at index %d comes before its parent %s at %d", m.Path, i, parent, pi)
			}
		}
		seen[m.Path] = i
	}
	// A child must never precede a parent it nests in.
	for i, m := range got {
		for j := i + 1; j < len(got); j++ {
			if strings.HasPrefix(m.Path, got[j].Path+"/") {
				t.Errorf("driver order %v: %s (i=%d) nests in %s (j=%d) but comes first",
					mountPaths(got), m.Path, i, got[j].Path, j)
			}
		}
	}
}

func mountPaths(ms []sandbox.Mount) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Path
	}
	return out
}

// CONTRACT: a mkmount source CREATES its host origin, and a mkmount into the
// box node's volume gives that box a private, PERSISTING slice of an otherwise
// shared tree — declared out of order, to prove ordering is not the mechanism.
func TestMkmountCreatesOriginAndNestsOverSharedMount(t *testing.T) {
	y := `recipes:
  r:
    image: img
    command: [run]
    sources:
      - mkmount: $NODE_VOLUME/sessions
        path: /cfg/sessions
      - mount: /home/t/vault
        path: /cfg
`
	fd := baseData()
	fd.exists["/home/t/vault"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "r"}); err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	up := onlyUp(t, drv)

	var sessions sandbox.Mount
	for _, m := range up.Mounts {
		if m.Path == "/cfg/sessions" {
			sessions = m
		}
	}
	if sessions.Host == "" {
		t.Fatalf("no mount at /cfg/sessions: %+v", up.Mounts)
	}
	// $NODE_VOLUME resolved to this box node's volume space — which SURVIVES down.
	if !strings.HasPrefix(sessions.Host, "/home/t/.dabs/nodes/") ||
		!strings.HasSuffix(sessions.Host, "/volume/sessions") {
		t.Errorf("mkmount host = %q, want the box node's volume space", sessions.Host)
	}
	// The engine created it: a mount whose origin is absent is a typo; a mkmount
	// whose origin is absent is the whole point.
	made := false
	for _, d := range fd.mkdirs {
		if d == sessions.Host {
			made = true
		}
	}
	if !made {
		t.Errorf("mkmount did not create %q; mkdirs=%v", sessions.Host, fd.mkdirs)
	}
	// It nests over the shared /cfg mount regardless of declaration order.
	for i, m := range up.Mounts {
		if m.Path == "/cfg" {
			for j, n := range up.Mounts {
				if n.Path == "/cfg/sessions" && j < i {
					t.Errorf("mkmount at /cfg/sessions (%d) precedes /cfg (%d): %v", j, i, mountPaths(up.Mounts))
				}
			}
		}
	}
}

// CONTRACT: a mount whose origin is missing is refused, and the error names the
// source kind that means "create it" — so the fix is discoverable, not folklore.
func TestMissingMountOriginPointsAtMkmount(t *testing.T) {
	y := `recipes:
  r:
    image: img
    command: [run]
    sources:
      - mount: /home/t/nope
        path: /cfg
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "r"})
	if err == nil {
		t.Fatal("want an error for a missing mount origin")
	}
	if !strings.Contains(err.Error(), "mkmount") {
		t.Errorf("error %q does not point at mkmount:", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("a box was booted despite the bad source: %+v", drv.ups)
	}
}

// CONTRACT: a recipe whose `.` source is a COPY mints a NEW workdir node every
// run — the same way a worktree recipe cuts a new branch every run — and it needs
// NO GIT to do it. Two runs over one directory give two independent places, which
// is what lets them be worked in parallel. A LIVE mount is the opposite: the place
// IS the host directory, so reaching it again is the same node.
//
// The fake has no git at all (GitToplevel errors for every dir), so this cannot
// pass by accident through a worktree.
func TestCopyRecipeMintsAFreshWorkdirEveryRunWithoutGit(t *testing.T) {
	fd := baseData()
	fd.exists["/cwd"] = true
	// No entry in fd.toplevel: every GitToplevel call errors. There is no repo.

	copyY := `recipes:
  d:
    image: img
    command: [x]
    sources:
      - copy: .
        path: /work
`
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	real := newReal(copyY, fd, drv)
	for i := 0; i < 2; i++ {
		if err := real.Recipe(params.Recipe{Name: "d"}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	wds := workdirNodes(t, fd)
	if len(wds) != 2 {
		t.Fatalf("two copy runs made %d workdir nodes, want 2 (one place per run): %v", len(wds), wds)
	}
	if wds[0] == wds[1] {
		t.Fatalf("both runs reused one workdir node %q; parallel runs would share a directory", wds[0])
	}

	// A LIVE mount provisions no middle node at all: the box stands directly on
	// the project, so no run makes a workdir.
	fd2 := baseData()
	fd2.exists["/cwd"] = true
	mountY := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: .
        path: /work
`
	drv2 := &fakeDriver{built: map[string]bool{"img": true}}
	real2 := newReal(mountY, fd2, drv2)
	for i := 0; i < 2; i++ {
		if err := real2.Recipe(params.Recipe{Name: "m"}); err != nil {
			t.Fatalf("mount run %d: %v", i, err)
		}
	}
	if got := workdirNodes(t, fd2); len(got) != 0 {
		t.Errorf("mount made %d workdir nodes, want 0 (the box stands on the project): %v", len(got), got)
	}
}

// workdirNodes reads back the workdir nodes the engine wrote into the fake.
func workdirNodes(t *testing.T, fd *fakeData) []string {
	t.Helper()
	var out []string
	for path, b := range fd.files {
		if !strings.HasSuffix(path, "/dabs-node.json") {
			continue
		}
		var n struct{ ID, Kind string }
		if json.Unmarshal(b, &n) == nil && n.Kind == "workdir" {
			out = append(out, n.ID)
		}
	}
	sort.Strings(out)
	return out
}

// nodeRec is the subset of a node record these tests assert on.
type nodeRec struct{ ID, Kind, Parent string }

func allNodeRecs(fd *fakeData) []nodeRec {
	var out []nodeRec
	for path, b := range fd.files {
		if !strings.HasSuffix(path, "/dabs-node.json") {
			continue
		}
		var n nodeRec
		if json.Unmarshal(b, &n) == nil {
			out = append(out, n)
		}
	}
	return out
}

func oneOfKind(t *testing.T, ns []nodeRec, kind string) nodeRec {
	t.Helper()
	var hits []nodeRec
	for _, n := range ns {
		if n.Kind == kind {
			hits = append(hits, n)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one %s node, got %d: %v", kind, len(hits), ns)
	}
	return hits[0]
}

// CONTRACT: a live `mount: .` provisions NO middle workdir — the box stands
// directly on the project (the diamond's direct edge). Only copy/worktree add a
// place between project and box.
func TestMountBoxParentsOnProjectNotAWorkdir(t *testing.T) {
	fd := baseData()
	fd.exists["/cwd"] = true
	// keep: true — a non-keep boot fully reaps its box node after the command
	// (#59), and this pin reads that record's parent.
	y := `recipes:
  m:
    image: img
    command: [x]
    keep: true
    sources:
      - mount: .
        path: /work
`
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	if wd := workdirNodes(t, fd); len(wd) != 0 {
		t.Fatalf("mount made workdir nodes %v, want none", wd)
	}
	nodes := allNodeRecs(fd)
	box := oneOfKind(t, nodes, "box")
	proj := oneOfKind(t, nodes, "project")
	if box.Parent != proj.ID {
		t.Fatalf("box parent = %q, want the project %q directly (no workdir between)", box.Parent, proj.ID)
	}
}

// CONTRACT: `recipes --print` covers the FULL merged registry — a recipe from
// ~/.dabs/recipes.yaml or ./dabs.yaml appears, sources and all, marked with
// its origin — and `--print <name>` prints just that recipe.
func TestRecipesPrintCoversMergedRegistry(t *testing.T) {
	global := `recipes:
  mybox:
    image: img
    command: [run]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.files = map[string][]byte{"dabs.yaml": []byte(`recipes:
  projbox:
    image: img
    command: [go]
    sources:
      - mount: .
        path: /work
`)}
	r := newReal(global, fd, &fakeDriver{})

	out := captureStdout(t, func() {
		if err := r.Recipes(params.Recipes{Print: true}); err != nil {
			t.Fatalf("recipes --print: %v", err)
		}
	})
	for _, want := range []string{"mybox", "~/.dabs/recipes.yaml", "projbox", "./dabs.yaml", "sh:", "bundled", "mount: /data"} {
		if !strings.Contains(out, want) {
			t.Fatalf("recipes --print missing %q in:\n%s", want, out)
		}
	}

	out = captureStdout(t, func() {
		if err := r.Recipes(params.Recipes{Print: true, Name: "projbox"}); err != nil {
			t.Fatalf("recipes --print projbox: %v", err)
		}
	})
	if !strings.Contains(out, "projbox") || strings.Contains(out, "mybox") || strings.Contains(out, "sh:") {
		t.Fatalf("recipes --print <name> should print only that recipe, got:\n%s", out)
	}

	err := r.Recipes(params.Recipes{Print: true, Name: "ghost"})
	if err == nil || !strings.Contains(err.Error(), `no recipe "ghost"`) {
		t.Fatalf("unknown name: want a no-recipe error listing the known ones, got %v", err)
	}
}

// CONTRACT: the plain `recipes` listing marks each recipe's origin layer.
func TestRecipesListMarksOrigin(t *testing.T) {
	fd := baseData()
	fd.files = map[string][]byte{"dabs.yaml": []byte(`recipes:
  projbox:
    image: img
    command: [go]
    sources: [{mount: ., path: /work}]
`)}
	out := captureStdout(t, func() {
		if err := newReal("", fd, &fakeDriver{}).Recipes(params.Recipes{}); err != nil {
			t.Fatalf("recipes: %v", err)
		}
	})
	if !strings.Contains(out, "project") || !strings.Contains(out, "bundled") {
		t.Fatalf("listing should mark origins (project/bundled), got:\n%s", out)
	}
}

// CONTRACT: the look-before-run preview preserves argv boundaries — an
// argument holding spaces is quoted, so `sh -c 'echo hi'` never reads as four
// words.
func TestRecipeConfirmQuotesArgv(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [sh, -c]
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.exists["/data"] = true
	var prompt string
	r := newReal(y, fd, &fakeDriver{built: map[string]bool{"img": true}}).WithConfirm(func(s string) bool {
		prompt = s
		return false // looking, not running
	})
	err := r.Recipe(params.Recipe{Args: []string{"m", "echo hi"}})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("declined confirm should abort, got %v", err)
	}
	if !strings.Contains(prompt, "command: sh -c 'echo hi'") {
		t.Fatalf("preview must quote the spaced argument, got:\n%s", prompt)
	}
}

// newRealNoDriver builds actions over a NIL local driver: any driver call at
// all is a nil-interface panic, so a passing test PROVES the command ran fully
// driverless.
func newRealNoDriver(recipesYAML string, fd *fakeData) actions.Real {
	if fd.files == nil {
		fd.files = map[string][]byte{}
	}
	if recipesYAML != "" {
		fd.files[fd.home+"/.dabs/recipes.yaml"] = []byte(recipesYAML)
	}
	return actions.New(map[string]sandbox.Driver{"local": nil}, []string{"local"}, fstest.MapFS{}, fd)
}

// CONTRACT: commands that only read the recipe registry or the node store —
// recipes (list and --print), cd (dir and spaces), worktrees (ls and diff) —
// run with NO driver at all.
func TestRegistryAndStoreCommandsRunDriverless(t *testing.T) {
	fd := baseData()
	seedWorktreeNode(fd, "wt-abcd", wtState{branch: "b"})
	r := newRealNoDriver("", fd)
	captureStdout(t, func() {
		if err := r.Recipes(params.Recipes{}); err != nil {
			t.Fatalf("recipes: %v", err)
		}
		if err := r.Recipes(params.Recipes{Print: true}); err != nil {
			t.Fatalf("recipes --print: %v", err)
		}
		if err := r.Cd(params.Cd{Node: "wt-abcd"}); err != nil {
			t.Fatalf("cd: %v", err)
		}
		if err := r.Worktrees(params.Worktrees{}); err != nil {
			t.Fatalf("worktrees ls: %v", err)
		}
		if err := r.Worktrees(params.Worktrees{Sub: "diff", Name: "wt-abcd"}); err != nil {
			t.Fatalf("worktrees diff: %v", err)
		}
	})
}

// CONTRACT: a BOXLESS recipe — one with no image, provisioning only places (a
// worktree cut, a directory copy) — runs with NO driver: whether a recipe
// needs one is decided by what it boots, not by the verb.
func TestBoxlessRecipesRunDriverless(t *testing.T) {
	y := `recipes:
  snap:
    description: copy the cwd into a place
    sources:
      - copy: .
  cut:
    description: cut a worktree, no box
    sources:
      - worktree: .
`
	fd := baseData()
	fd.toplevel["/cwd"] = nil // the cwd is a git repo with commits
	r := newRealNoDriver(y, fd)
	captureStdout(t, func() {
		if err := r.Recipe(params.Recipe{Name: "snap"}); err != nil {
			t.Fatalf("boxless copy recipe: %v", err)
		}
		if err := r.Recipe(params.Recipe{Name: "cut"}); err != nil {
			t.Fatalf("boxless worktree recipe: %v", err)
		}
	})
	if len(fd.worktrees) == 0 {
		t.Fatal("the worktree recipe cut nothing")
	}
}

// CONTRACT: running a provisioning command from a LINKED git worktree does not
// mint a project — that place is a worktree. The chain root is an
// externally-managed worktree marker (kind worktree, Dir the checkout, no
// worktree nest), parented under the repo's project node when dabs tracks one.
func TestProvisionFromLinkedWorktreeMintsWorktreeMarker(t *testing.T) {
	y := `recipes:
  snap:
    description: copy the cwd into a place
    sources:
      - copy: .
`
	fd := baseData()
	fd.cwd = "/mainrepo/.claude/worktrees/agent-x"
	fd.files = map[string][]byte{
		nodeBase + "/mainrepo-aaaa/dabs-node.json": []byte(
			`{"id":"mainrepo-aaaa","kind":"project","dir":"/mainrepo","recipe":"r","created":"t"}`),
	}
	fd.dirs = map[string][]string{nodeBase: {"mainrepo-aaaa"}}
	// The cwd is a git checkout whose common dir lives elsewhere: a LINKED worktree.
	fd.toplevel[fd.cwd] = nil
	fd.commondir = map[string]string{fd.cwd: "/mainrepo/.git"}

	r := newRealNoDriver(y, fd)
	captureStdout(t, func() {
		if err := r.Recipe(params.Recipe{Name: "snap"}); err != nil {
			t.Fatalf("boxless recipe from a linked worktree: %v", err)
		}
	})

	var marker map[string]any
	for p, b := range fd.files {
		if !strings.HasSuffix(p, "/dabs-node.json") {
			continue
		}
		var n map[string]any
		if err := json.Unmarshal(b, &n); err != nil {
			continue
		}
		if n["dir"] == fd.cwd {
			if marker != nil {
				t.Fatalf("two nodes minted for the worktree dir")
			}
			marker = n
		}
	}
	if marker == nil {
		t.Fatalf("no chain-root marker minted for the worktree dir; records: %v", fd.files)
	}
	if marker["kind"] != "worktree" {
		t.Errorf("marker kind = %v, want worktree (a linked worktree is not a project)", marker["kind"])
	}
	if marker["worktree"] != nil {
		t.Errorf("marker must carry no worktree nest (the checkout is externally managed): %v", marker)
	}
	if marker["parent"] != "mainrepo-aaaa" {
		t.Errorf("marker parent = %v, want the repo's tracked project mainrepo-aaaa", marker["parent"])
	}
}

// boxNodeParent returns the parent recorded on the single box node dabs wrote,
// failing the test if there is not exactly one — the parentage a box booted with.
func boxNodeParent(t *testing.T, fd *fakeData) string {
	t.Helper()
	parent, found := "", 0
	for p, b := range fd.files {
		if !strings.HasSuffix(p, "/dabs-node.json") {
			continue
		}
		var n map[string]any
		if json.Unmarshal(b, &n) != nil {
			continue
		}
		if n["kind"] == "box" {
			found++
			parent, _ = n["parent"].(string)
		}
	}
	if found != 1 {
		t.Fatalf("want exactly one box node, found %d (records: %v)", found, fd.files)
	}
	return parent
}

// CONTRACT: booting a box from inside a dabs worktree's OWN checkout (a cwd under
// ~/.dabs/nodes/<id>/held/worktree) parents the box on that worktree — the same
// outcome as an explicit --worktree — instead of refusing as "inside dabs's own
// storage". The checkout is mounted at the `.` source and the parent .git rides
// along so git works in-box.
func TestBoxFromWorktreeCheckoutParentsOnWorktree(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: .
        path: /work
`
	fd := baseData()
	wt := seedWorktreeNode(fd, "wt1", wtState{branch: "dabs/wt1"})
	fd.cwd = wt                    // the cwd IS the worktree's checkout
	fd.exists["/repo/.git"] = true // the parent store exists (a real mount)
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"}); err != nil {
		t.Fatalf("recipe from worktree checkout: %v", err)
	}
	// It must NOT fork a fresh worktree, and it must NOT refuse.
	if len(fd.worktrees) != 0 {
		t.Fatalf("booting from a checkout forked a worktree, want none: %v", fd.worktrees)
	}
	up := onlyUp(t, drv)
	want := []sandbox.Mount{
		{Host: wt, Path: "/work"},                // the checkout, mounted live
		{Host: "/repo/.git", Path: "/repo/.git"}, // parent store, so git works in-box
	}
	if len(up.Mounts) != len(want) {
		t.Fatalf("mounts = %+v, want %+v", up.Mounts, want)
	}
	for _, w := range want {
		found := false
		for _, got := range up.Mounts {
			if got == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing mount %+v; got %+v", w, up.Mounts)
		}
	}
	// A non-keep box is torn down (its node reaped) on exit, so the surviving proof
	// of parentage is the worktree-up journal: the box was linked to wt1's checkout.
	if !strings.Contains(string(fd.files[logPath]), `"worktree":"wt1"`) {
		t.Errorf("box was not journalled against worktree wt1: %s", fd.files[logPath])
	}
}

// CONTRACT: the same parenting holds for the `--detach` path — and the box node,
// which `--detach` keeps, records the worktree as its parent.
func TestDetachBoxFromWorktreeCheckoutParentsOnWorktree(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: .
        path: /work
`
	fd := baseData()
	wt := seedWorktreeNode(fd, "wt1", wtState{branch: "dabs/wt1"})
	fd.cwd = wt
	fd.exists["/repo/.git"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	captureStdout(t, func() {
		if err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true, Args: []string{"m"}}); err != nil {
			t.Fatalf("recipe --detach from worktree checkout: %v", err)
		}
	})
	if len(fd.worktrees) != 0 {
		t.Fatalf("--detach from a checkout forked a worktree: %v", fd.worktrees)
	}
	if got := boxNodeParent(t, fd); got != "wt1" {
		t.Errorf("box parent = %q, want the worktree node wt1", got)
	}
}

// CONTRACT: booting a box from a cwd under ~/.dabs that is NOT inside any known
// worktree's checkout stays refused — there is no node to parent onto.
func TestBoxFromDabsStoreOutsideAnyWorktreeRefused(t *testing.T) {
	y := `recipes:
  m:
    image: img
    command: [x]
    sources:
      - mount: .
        path: /work
`
	fd := baseData()
	fd.cwd = "/home/t/.dabs/images" // dabs's storage, but no node owns it
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Name: "m"})
	if err == nil || !strings.Contains(err.Error(), "dabs's own storage") {
		t.Fatalf("want a refuse-inside-storage error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("box brought up from unowned dabs storage: %v", drv.ups)
	}
}

// CONTRACT: cutting a WORKTREE from inside a worktree's checkout stays refused —
// a worktree of a checkout is nonsense — and the message names what is refused.
func TestWorktreeCreationFromCheckoutRefused(t *testing.T) {
	y := `recipes:
  cut:
    sources:
      - worktree: .
`
	fd := baseData()
	wt := seedWorktreeNode(fd, "wt1", wtState{branch: "dabs/wt1"})
	fd.cwd = wt
	err := newRealNoDriver(y, fd).Recipe(params.Recipe{Name: "cut"})
	if err == nil || !strings.Contains(err.Error(), "dabs's own storage") || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("want a refusal naming worktree/storage, got %v", err)
	}
	if len(fd.worktrees) != 0 {
		t.Errorf("cut a worktree from inside a checkout: %v", fd.worktrees)
	}
}
