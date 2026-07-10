//go:build e2e

// End-to-end tests that drive the real `dabs` CLI as a plain command on PATH —
// not imported as a library, not behind a helper that hides the binary. The
// suite only runs inside its dabs box — a docker container (DABS_NAME set and
// /.dockerenv present); anywhere else it exits without running. The box is the
// isolation and must carry `dabs` on PATH. Inside, the boxes dabs creates are
// exercised in place — dabs picks the platform's driver, the suite never does.
// Per-run isolation is a fresh $HOME (its own ~/.dabs), removed on teardown,
// plus unique "dabs-e2e-*" box names.
//
// Run:  inside a dabs box that has `dabs` on PATH (see run_e2e.sh).
// Prebuilt mode (DABS_E2E_PREBUILT=<dir>): stage a base image instead of
// building it, so no docker is needed inside the box.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	home    string // isolated HOME for this run
	baseDir string // this package dir: holds the base image dabs.json + Dockerfile
)

const sandboxName = "dabs-e2e"

// --- setup / teardown --------------------------------------------------------

func TestMain(m *testing.M) { os.Exit(setupAndRun(m)) }

func setupAndRun(m *testing.M) int {
	// Only run inside the supported box — a dabs-created docker container.
	// Two checks: DABS_NAME marks a dabs box; /.dockerenv reliably marks a
	// docker container (deliberate coupling — the supported box is docker).
	_, inDocker := os.Stat("/.dockerenv")
	if os.Getenv("DABS_NAME") == "" || inDocker != nil {
		fmt.Fprintln(os.Stderr, "e2e: this suite runs only inside its dabs docker box; "+
			"run ./run_e2e.sh (running `go test` directly won't work)")
		return 1
	}
	var err error
	home, err = os.MkdirTemp("", "dabs-e2e-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	defer os.RemoveAll(home) // teardown: isolated HOME and everything under it
	os.Setenv("HOME", home)

	// The base image the inner boxes come from is this package's own
	// dabs.json + Dockerfile (name "dabs-e2e"); build it once with the source
	// dabs and reuse it across tests.
	baseDir, err = os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	// Prebuilt mode (DABS_E2E_PREBUILT=<staged image dir>): copy a pre-staged
	// base image into this run's isolated HOME instead of building it. This is
	// what lets the suite run in a plain, UNPRIVILEGED container with no in-box
	// docker (the build path needs docker; the bwrap run path does not).
	if pre := os.Getenv("DABS_E2E_PREBUILT"); pre != "" {
		dst := filepath.Join(home, ".dabs", "images", "dabs-e2e")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "setup:", err)
			return 1
		}
		if out, err := exec.Command("cp", "-a", pre, dst).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "setup: stage prebuilt image: %v\n%s\n", err, out)
			return 1
		}
	} else if out, code := run("dabs build " + baseDir); code != 0 {
		fmt.Fprintf(os.Stderr, "setup: dabs build base failed:\n%s\n", out)
		return 1
	}
	return m.Run()
}

// --- helpers -----------------------------------------------------------------

// run executes a plain command line (argv[0] resolved on PATH) and returns
// combined output + exit code. Call sites read as the shell line they run.
func run(cmdline string) (string, int) {
	argv := splitArgs(cmdline)
	cmd := exec.Command(argv[0], argv[1:]...)
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

// runIn is run with an explicit working directory, for cwd-sensitive commands
// like `dabs claude` (which keys off the git repo containing the cwd).
func runIn(dir, cmdline string) (string, int) {
	argv := splitArgs(cmdline)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

// runTimeout runs a command with a deadline; the bool reports whether it timed
// out (i.e. hung). Used to prove a command returns instead of blocking.
func runTimeout(d time.Duration, cmdline string) (string, bool) {
	argv := splitArgs(cmdline)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, _ := cmd.CombinedOutput()
	return string(out), ctx.Err() == context.DeadlineExceeded
}

func runStdin(stdin, cmdline string) string {
	argv := splitArgs(cmdline)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// splitArgs splits a command line on whitespace, keeping single/double-quoted
// runs (with their spaces) as one argument and stripping the quotes — enough
// to write a `sh -c '…'` command as one readable string.
func splitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	var quote byte // 0, '\'' or '"'
	inArg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inArg = c, true
		case c == ' ' || c == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
			continue
		default:
			cur.WriteByte(c)
		}
		inArg = true
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
}

func writeManifest(dir, name, dockerfile string) {
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "dabs.json"),
		[]byte(fmt.Sprintf("{\"name\":%q,\"env\":{\"E2E\":\"yes\"}}\n", name)), 0o644)
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644)
}

// clean reaps every "base" instance now and again at test end, so tests don't
// see each other's boxes (they share the isolated HOME).
func clean(t *testing.T) {
	t.Helper()
	run("dabs down " + sandboxName + " --force")
	t.Cleanup(func() { run("dabs down " + sandboxName + " --force") })
}

// up starts a fresh base instance and returns its full name.
func up(t *testing.T) string {
	t.Helper()
	out, code := run("dabs up " + baseDir)
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], sandboxName+"-") {
		t.Fatalf("up printed unexpected: %q", out)
	}
	return fields[0]
}

func wantExit(t *testing.T, want, got int) {
	t.Helper()
	if want != got {
		t.Fatalf("exit code: want %d got %d", want, got)
	}
}

func wantContains(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Fatalf("expected %q in output:\n%s", want, out)
	}
}

func wantNotContains(t *testing.T, out, bad string) {
	t.Helper()
	if strings.Contains(out, bad) {
		t.Fatalf("did not expect %q in output:\n%s", bad, out)
	}
}

// --- dispatch ----------------------------------------------------------------

func TestUsageNoArgs(t *testing.T) {
	out, code := run("dabs")
	wantExit(t, 2, code)
	wantContains(t, out, "usage: dabs")
}

func TestUnknownCommand(t *testing.T) {
	out, code := run("dabs bogus")
	wantExit(t, 2, code)
	wantContains(t, out, "unknown command")
}

// --- build -------------------------------------------------------------------

func TestBuild(t *testing.T) {
	if os.Getenv("DABS_E2E_PREBUILT") != "" {
		t.Skip("prebuilt mode: docker build path is tested on the host, not in-box")
	}
	d := filepath.Join(home, "bt")
	writeManifest(d, "bt", "FROM alpine:3.20\nWORKDIR /work\n")
	out, code := run("dabs build " + d)
	wantExit(t, 0, code)
	wantContains(t, out, "bt built")
}

// --- up / ls -----------------------------------------------------------------

func TestUpPrintsInstance(t *testing.T) {
	clean(t)
	out, code := run("dabs up " + baseDir)
	wantExit(t, 0, code)
	wantContains(t, out, sandboxName+"-")
	wantContains(t, out, " up")
}

func TestUpCreatesDistinctInstances(t *testing.T) {
	clean(t)
	a, b := up(t), up(t)
	if a == b {
		t.Fatalf("two ups gave the same instance: %s", a)
	}
	out, _ := run("dabs ls")
	wantContains(t, out, a)
	wantContains(t, out, b)
}

func TestLsEmpty(t *testing.T) {
	clean(t)
	out, _ := run("dabs ls")
	wantContains(t, out, "local")
	wantContains(t, out, "this machine")
	wantNotContains(t, out, sandboxName+"-") // none of OUR instances
}

func TestLsShowsInstanceAndDriver(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs ls")
	wantContains(t, out, i)
}

// --- run ---------------------------------------------------------------------

func TestRunEnvAndWorkdir(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs run " + i +
		" -- sh -c 'echo E2E=$E2E cwd=$(pwd); cat /work/marker.txt'")
	wantContains(t, out, "E2E=yes")
	wantContains(t, out, "cwd=/work")
	wantContains(t, out, "hello-from-image")
}

func TestRunPrefixResolves(t *testing.T) {
	clean(t)
	i := up(t)
	prefix := i[:len(i)-6] // drop 6 hex chars; unique with a single instance
	out, _ := run("dabs run " + prefix + " -- echo prefix-ok")
	wantContains(t, out, "prefix-ok")
}

func TestRunAmbiguous(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	out, _ := run("dabs run " + sandboxName + " -- echo x")
	wantContains(t, out, "ambiguous")
}

func TestRunMissing(t *testing.T) {
	out, _ := run("dabs run nope-missing -- echo x")
	wantContains(t, out, "no instance matches")
}

func TestRunIsolationBetweenInstances(t *testing.T) {
	clean(t)
	a, b := up(t), up(t)
	run("dabs run " + a + " -- touch /work/only-in-a")
	out, _ := run("dabs run " + b + " -- ls /work")
	wantNotContains(t, out, "only-in-a")
}

func TestRunPersistenceWithinInstance(t *testing.T) {
	clean(t)
	i := up(t)
	run("dabs run " + i + " -- touch /work/persisted")
	out, _ := run("dabs run " + i + " -- ls /work")
	wantContains(t, out, "persisted")
}

// --- down --------------------------------------------------------------------

func TestDownOne(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs down " + i)
	wantContains(t, out, i+" down")
}

func TestDownDryListsAndKeeps(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	out, _ := run("dabs down " + sandboxName + " --dry")
	wantContains(t, out, "matches")
	ls, _ := run("dabs ls")
	if strings.Count(ls, sandboxName+"-") != 2 {
		t.Fatalf("expected 2 instances after --dry, got:\n%s", ls)
	}
}

func TestDownForceReapsAll(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	run("dabs down " + sandboxName + " --force")
	out, _ := run("dabs ls")
	wantNotContains(t, out, sandboxName+"-")
}

func TestDownMissingIsNotError(t *testing.T) {
	out, code := run("dabs down nope-missing")
	wantExit(t, 0, code)
	wantContains(t, out, "nothing matches")
}

// --- mcp ---------------------------------------------------------------------

func TestMcpToolsListAndCall(t *testing.T) {
	clean(t)
	i := up(t)
	req := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"dabash","arguments":{"command":"cat /work/marker.txt"}}}`,
	}, "\n") + "\n"
	out := runStdin(req, "dabs mcp "+i)
	wantContains(t, out, `"name":"dabash"`)
	wantContains(t, out, "hello-from-image")
}

// --- auth --------------------------------------------------------------------

// TestAuthClaudeCapturesCredential drives `dabs auth claude` against the FAKE
// claude baked into the prebuilt base image (a real CLI named `claude` that
// only "logs in" by writing a credential). It proves the live vault mount
// captures that credential onto the host: the login writes inside the box, and
// it lands in the host vault (~/.dabs/auth/claude) because the vault is
// bind-mounted read-write. This is the driver's mount support under test —
// bwrap in the runner. DABS_AUTH_IMAGE reuses the prebuilt image, so nothing
// is built in-box.
func TestAuthClaudeCapturesCredential(t *testing.T) {
	clean(t)
	t.Setenv("DABS_AUTH_IMAGE", sandboxName) // reuse the prebuilt base image

	out, code := run("dabs auth claude")
	wantExit(t, 0, code)
	wantContains(t, out, "claude authenticated")

	cred := filepath.Join(home, ".dabs", "auth", "claude", ".credentials.json")
	data, err := os.ReadFile(cred)
	if err != nil {
		t.Fatalf("credential not captured to vault: %v", err)
	}
	wantContains(t, string(data), "fake-token")
}

// --- recipes -----------------------------------------------------------------

// The e2e recipes: three made-up recipes that each place the code into /work a
// different way (mount / copy / worktree) and mount the auth vault, then run the
// FAKE `claude` (which writes a credential and exits). They use the prebuilt
// base image (dabs-e2e) so nothing is built in-box. Written to the user's
// ~/.dabs/recipes.yaml, so this also exercises user-registry override + merge.
const e2eRecipes = `recipes:
  claude-mounted:
    image: dabs-e2e
    command: [sh, -c, "echo box-was-here > /work/from-box.txt; claude"]
    sources:
      - mount: ~/.dabs/auth/claude
        path: /root/.claude
      - mount: .
        path: /work
  claude-isolated:
    image: dabs-e2e
    command: [sh, -c, "cat /work/seed.txt; echo box > /work/from-box.txt; claude"]
    sources:
      - mount: ~/.dabs/auth/claude
        path: /root/.claude
      - copy: .
        path: /work
  claude-new-worktree:
    image: dabs-e2e
    command: [sh, -c, "echo box-was-here > /work/from-box.txt; claude"]
    sources:
      - mount: ~/.dabs/auth/claude
        path: /root/.claude
      - worktree: .
        path: /work
  shellhang:
    image: dabs-e2e
    command: [sh]
`

// installRecipes writes the e2e recipes to ~/.dabs/recipes.yaml and ensures the
// auth vault dir exists (recipes mount it, so the bind needs a real host dir).
func installRecipes(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".dabs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".dabs", "recipes.yaml"), []byte(e2eRecipes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".dabs", "auth", "claude"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// gitRepo makes a committed git repo at dir with one file.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"git", "init", "-q"},
		{"git", "config", "user.email", "e2e@dabs.test"},
		{"git", "config", "user.name", "e2e"},
		{"git", "add", "-A"},
		{"git", "commit", "-qm", "init"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
}

func vaultHasToken(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".dabs", "auth", "claude", ".credentials.json"))
	if err != nil {
		t.Fatalf("vault mount failed: credential not written back to host: %v", err)
	}
	wantContains(t, string(data), "fake-token")
}

// TestRecipesLists proves `dabs recipes` shows the bundled `claude` recipe and
// the user's recipes together.
func TestRecipesLists(t *testing.T) {
	installRecipes(t)
	out, code := run("dabs recipes")
	wantExit(t, 0, code)
	wantContains(t, out, "claude")              // bundled
	wantContains(t, out, "fresh-claude")        // bundled (logged-out throwaway)
	wantContains(t, out, "claude-mounted")      // user
	wantContains(t, out, "claude-isolated")     // user
	wantContains(t, out, "claude-new-worktree") // user
}

// TestRecipeUnknownLists proves an unknown recipe fails clearly, listing what
// IS known (the caller is usually an agent that must choose a real one).
func TestRecipeUnknownLists(t *testing.T) {
	installRecipes(t)
	out, code := runIn(home, "dabs recipe nope")
	if code == 0 {
		t.Fatalf("want non-zero exit for unknown recipe; got 0\n%s", out)
	}
	wantContains(t, out, "no recipe \"nope\"")
	wantContains(t, out, "claude")
}

// TestRecipeMounted proves a `mount:` source is LIVE: a file the box writes into
// /work lands on the host, and the vault mount captures the fake login.
func TestRecipeMounted(t *testing.T) {
	clean(t)
	installRecipes(t)
	dir := filepath.Join(home, "mounted")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := runIn(dir, "dabs recipe claude-mounted")
	wantExit(t, 0, code)
	wantContains(t, out, "fake-claude: login ok")

	if _, err := os.Stat(filepath.Join(dir, "from-box.txt")); err != nil {
		t.Fatalf("mount not live: box's write did not reach the host dir: %v", err)
	}
	vaultHasToken(t)
}

// TestRecipeIsolated proves a `copy:` source is a SNAPSHOT: the box sees the
// copied-in file, but a file the box writes into /work does NOT reach the host.
func TestRecipeIsolated(t *testing.T) {
	clean(t)
	installRecipes(t)
	dir := filepath.Join(home, "isolated")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seeded-copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIn(dir, "dabs recipe claude-isolated")
	wantExit(t, 0, code)
	wantContains(t, out, "seeded-copy") // the copy delivered the host file into the box

	if _, err := os.Stat(filepath.Join(dir, "from-box.txt")); err == nil {
		t.Fatalf("copy not isolated: box's write leaked back to the host dir")
	}
	vaultHasToken(t)
}

// TestRecipeNewWorktree proves a `worktree:` source runs the box on a fresh
// branch off HEAD, kept after exit: the box's write lands in the worktree (not
// the original repo), and the worktree survives.
func TestRecipeNewWorktree(t *testing.T) {
	clean(t)
	installRecipes(t)
	repo := filepath.Join(home, "wtrepo")
	gitRepo(t, repo)

	out, code := runIn(repo, "dabs recipe claude-new-worktree")
	wantExit(t, 0, code)
	wantContains(t, out, "worktree kept")

	// The box's write landed in a KEPT worktree, not the original repo.
	if _, err := os.Stat(filepath.Join(repo, "from-box.txt")); err == nil {
		t.Fatalf("worktree not isolated: box's write appeared in the original repo")
	}
	entries, err := os.ReadDir(filepath.Join(home, ".dabs", "worktrees"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected a kept worktree under ~/.dabs/worktrees; err=%v entries=%v", err, entries)
	}
	wt := filepath.Join(home, ".dabs", "worktrees", entries[0].Name(), "from-box.txt")
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("box's write not found in the kept worktree: %v", err)
	}
	vaultHasToken(t)
}

// TestWorktreesInspectAndGuardedReap: a recipe leaves a worktree with the box's
// uncommitted write; `dabs worktrees` lists it as having work, `rm` refuses to
// discard it, and `rm --force` reaps it.
func TestWorktreesInspectAndGuardedReap(t *testing.T) {
	clean(t)
	installRecipes(t)
	os.RemoveAll(filepath.Join(home, ".dabs", "worktrees")) // isolate from other tests' kept worktrees
	repo := filepath.Join(home, "wtreap")
	gitRepo(t, repo)
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	entries, err := os.ReadDir(filepath.Join(home, ".dabs", "worktrees"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no worktree created: %v", err)
	}
	name := entries[0].Name()

	out, code := run("dabs worktrees")
	wantExit(t, 0, code)
	wantContains(t, out, "HAS WORK") // the box wrote from-box.txt (uncommitted)

	out, code = run("dabs worktrees rm " + name)
	if code == 0 {
		t.Fatalf("rm removed a worktree with unreviewed work without --force\n%s", out)
	}
	wantContains(t, out, "unreviewed work")

	out, code = run("dabs worktrees rm " + name + " --force")
	wantExit(t, 0, code)
	wantContains(t, out, "removed")
	if e, _ := os.ReadDir(filepath.Join(home, ".dabs", "worktrees")); len(e) != 0 {
		t.Fatalf("worktree not reaped after --force: %v", e)
	}
}

// TestRecipeLocalDabsYamlDefault: a project's ./dabs.yaml adds recipes and a
// default; `dabs recipe` with no name runs the default.
func TestRecipeLocalDabsYamlDefault(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "default: probe\nrecipes:\n  probe:\n    image: " + sandboxName +
		"\n    command: [sh, -c, \"echo LOCAL_DEFAULT_RAN\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIn(dir, "dabs recipes")
	wantExit(t, 0, code)
	wantContains(t, out, "probe")
	wantContains(t, out, "(default)")

	out, code = runIn(dir, "dabs recipe") // no name → default
	wantExit(t, 0, code)
	wantContains(t, out, "LOCAL_DEFAULT_RAN")
}

// --- cli documentation & robustness (dumb-user findings) ---------------------

// TestHelpRendersAndPointsToFull: `dabs --help` is not an error — it prints
// usage and points agents at the full guide, which `--help-full-for-agents`
// prints.
func TestHelpRendersAndPointsToFull(t *testing.T) {
	out, code := run("dabs --help")
	wantExit(t, 0, code)
	wantContains(t, out, "usage: dabs")
	wantContains(t, out, "--help-full-for-agents")
	full, code := run("dabs --help-full-for-agents")
	wantExit(t, 0, code)
	wantContains(t, full, "dabs box") // the bundled AGENTS.md guide
}

// TestUpBareNameHintsRecipe: `dabs up <name>` (not a manifest) fails with a hint
// that up takes a manifest and recipes are run with `dabs recipe`.
func TestUpBareNameHintsRecipe(t *testing.T) {
	out, code := run("dabs up not-a-manifest")
	if code == 0 {
		t.Fatalf("want non-zero exit; got 0\n%s", out)
	}
	wantContains(t, out, "manifest")
	wantContains(t, out, "recipe")
}

// TestManifestEnvTypeErrorIsFriendly: a wrong-typed field says what it should be,
// not the Go struct type.
func TestManifestEnvTypeErrorIsFriendly(t *testing.T) {
	d := filepath.Join(home, "badenv")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "dabs.json"), []byte(`{"name":"x","env":[1,2]}`), 0o644)
	out, code := run("dabs build " + d)
	if code == 0 {
		t.Fatalf("want non-zero exit; got 0\n%s", out)
	}
	wantContains(t, out, "must be an object")
	wantNotContains(t, out, "map[string]string")
}

// TestRecipesPrintShowsFormat: `dabs recipes --print` dumps the authoring YAML,
// so the format is discoverable without reverse-engineering the binary.
func TestRecipesPrintShowsFormat(t *testing.T) {
	out, code := run("dabs recipes --print")
	wantExit(t, 0, code)
	wantContains(t, out, "recipes:")
	wantContains(t, out, "sources:")
}

// TestRecipeInteractiveDoesNotHang: a recipe whose command is a bare interactive
// shell must EXIT when run without a terminal, not block forever.
func TestRecipeInteractiveDoesNotHang(t *testing.T) {
	clean(t)
	installRecipes(t)
	out, timedOut := runTimeout(45*time.Second, "dabs recipe shellhang")
	if timedOut {
		t.Fatalf("`dabs recipe` with an interactive shell hung with no TTY:\n%s", out)
	}
}

// --- servers -----------------------------------------------------------------

// TestServersAddRejectsFlagName: a flag-shaped name is rejected, not silently
// stored as a server literally named "--help".
func TestServersAddRejectsFlagName(t *testing.T) {
	out, code := run("dabs servers add --help")
	if code == 0 {
		t.Fatalf("want non-zero exit; got 0\n%s", out)
	}
	wantContains(t, out, "cannot start with")
	ls, _ := run("dabs servers ls")
	wantNotContains(t, ls, "--help")
}

func TestServersEmptyShowsLocalOnly(t *testing.T) {
	out, _ := run("dabs servers ls")
	wantContains(t, out, "local")
	wantContains(t, out, "this machine")
}

func TestServersAddAndList(t *testing.T) {
	run("dabs servers add s1 host1.example")
	t.Cleanup(func() { run("dabs servers rm s1") })
	out, _ := run("dabs servers ls")
	wantContains(t, out, "s1")
	wantContains(t, out, "ssh host1.example")
}

func TestServersRemove(t *testing.T) {
	run("dabs servers add s2 host2")
	run("dabs servers rm s2")
	out, _ := run("dabs servers ls")
	wantNotContains(t, out, "s2")
}

// --- install / uninstall -----------------------------------------------------

func TestInstallBarePrintsInstructions(t *testing.T) {
	out, _ := run("dabs install")
	wantContains(t, out, "dabs install <harness>")
}

func TestInstallAndUninstallClaude(t *testing.T) {
	runStdin("y\n", "dabs install claude")
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/dabs/SKILL.md")); err != nil {
		t.Fatalf("claude skill not installed: %v", err)
	}
	runStdin("y\n", "dabs uninstall claude")
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/dabs")); !os.IsNotExist(err) {
		t.Fatalf("claude skill not removed")
	}
}

func TestInstallPi(t *testing.T) {
	runStdin("y\n", "dabs install pi")
	t.Cleanup(func() { runStdin("y\n", "dabs uninstall pi") })
	if _, err := os.Stat(filepath.Join(home, ".pi/extensions/dabash/index.ts")); err != nil {
		t.Fatalf("pi extension not installed: %v", err)
	}
}

// --- env marker --------------------------------------------------------------

func TestDabsNamePresentInBox(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs run " + i + " -- sh -c 'echo DABS_NAME=$DABS_NAME'")
	wantContains(t, out, "DABS_NAME="+i)
}
