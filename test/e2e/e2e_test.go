//go:build e2e

// End-to-end tests that drive the real `dabs` CLI as a plain command on PATH —
// not imported as a library, not behind a helper that hides the binary. The
// suite only runs inside its dabs box — a docker container (DABS_NAME set and
// /.dockerenv present); anywhere else it exits without running. The box is the
// isolation and must carry `dabs` on PATH. Inside, the boxes dabs creates are
// exercised in place — dabs picks the platform's driver, the suite never does.
//
// Because the box is the isolation, the suite uses the box's own $HOME and its
// own ~/.dabs: every run gets a fresh box and the box is reaped after, so there
// is no isolated HOME to mint and nothing to clean up.
//
// Run:  ./run_e2e.sh  (running `go test` on your host will refuse to run).
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	home    string // the box's own HOME — the box is the isolation
	baseDir string // this package dir: holds the base recipe dabs.yaml + Dockerfile
)

const sandboxName = "dabs-e2e"

// --- setup / teardown --------------------------------------------------------

func TestMain(m *testing.M) { os.Exit(setupAndRun(m)) }

func setupAndRun(m *testing.M) int {
	// Only run somewhere sandboxed. The suite reaps boxes, rewrites ~/.dabs,
	// and drives whatever dabs it finds on PATH — a developer's host is not a
	// place for that. Two sandboxes qualify: the suite's own dabs docker box
	// (DABS_NAME + /.dockerenv — run_e2e.sh builds it), and a hermetic CI
	// runner (GITHUB_ACTIONS), already a throwaway machine that needs no
	// outer box.
	_, dockerErr := os.Stat("/.dockerenv")
	inDabsBox := os.Getenv("DABS_NAME") != "" && dockerErr == nil
	inCI := os.Getenv("GITHUB_ACTIONS") == "true"
	if !inDabsBox && !inCI {
		fmt.Fprintln(os.Stderr, "e2e: this suite runs only somewhere sandboxed — "+
			"its dabs docker box (./run_e2e.sh) or a CI runner; "+
			"running `go test` on your own machine won't work")
		return 1
	}
	// The box IS the isolation, so the box's own $HOME is this run's $HOME: every
	// run gets a fresh box, and the box is torn down after. Nothing to mint,
	// nothing to clean up.
	home = os.Getenv("HOME")
	if home == "" {
		fmt.Fprintln(os.Stderr, "setup: HOME is unset in the box")
		return 1
	}
	var err error
	baseDir, err = os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	// The base image the inner boxes come from is staged into the box by its
	// Dockerfile (COPY --from), because `dabs build` needs docker and the box
	// carries none. Fail loudly if it is missing rather than letting every test
	// fail one by one on a cryptic "image not built".
	if _, err := os.Stat(filepath.Join(home, ".dabs", "images", sandboxName)); err != nil {
		fmt.Fprintf(os.Stderr, "setup: base image %q not staged in this box (%v)\n"+
			"the box's Dockerfile stages it; rebuild the box: dabs build test/e2e/box\n", sandboxName, err)
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

// runInStdin runs in dir with stdin fed — for a cwd-sensitive command that also
// hits a confirm prompt (the default-recipe path always confirms).
func runInStdin(dir, stdin, cmdline string) (string, int) {
	argv := splitArgs(cmdline)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
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

// writeRecipe writes a minimal buildable recipe (a dabs.yaml + Dockerfile) into
// dir: one recipe named `name`, set as the default, whose image builds from the
// Dockerfile. It replaces the old dabs.json manifest fixtures now that build/up
// resolve recipes.
func writeRecipe(dir, name, dockerfile string) {
	os.MkdirAll(dir, 0o755)
	yaml := fmt.Sprintf("default: %s\nrecipes:\n  %s:\n    image: { dockerfile: Dockerfile, context: . }\n    env: { E2E: \"yes\" }\n", name, name)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644)
}

// clean reaps every "base" instance now and again at test end, so tests don't
// see each other's boxes (one box, one ~/.dabs).
// clean reaps every box this suite made. The name is a PREFIX matching many
// instances, so it needs --multiple (the explicit approval to act on more than
// one) as well as --yes (skip the consent prompt) — neither alone reaps a fleet.
func clean(t *testing.T) {
	t.Helper()
	run("dabs rm " + sandboxName + " --multiple --yes")
	t.Cleanup(func() { run("dabs rm " + sandboxName + " --multiple --yes") })
}

// up starts a fresh base instance and returns its full name.
func up(t *testing.T) string {
	t.Helper()
	out, code := run("dabs recipe " + baseDir + " --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	// `up` reports the canonical NODE ID on its `id:` line and the driver INSTANCE
	// name on its `instance:` line. Callers here drive rm/exec/ls, all of which
	// resolve either — return the instance so the running box is what they name.
	for _, line := range strings.Split(out, "\n") {
		if inst, ok := strings.CutPrefix(strings.TrimSpace(line), "instance: "); ok {
			return strings.TrimSpace(inst)
		}
	}
	t.Fatalf("up printed no instance line: %q", out)
	return ""
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

// hasRecipeLine reports whether `dabs recipes` output lists a recipe named
// exactly name on its own row (first whitespace field) — not a substring buried
// in another recipe's name or description (e.g. "sh" inside "shell").
func hasRecipeLine(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return true
		}
	}
	return false
}

// knownListsRecipe reports whether an error's "(known: a, b, c)" list names the
// recipe as a distinct entry, not just a substring.
func knownListsRecipe(out, name string) bool {
	_, after, ok := strings.Cut(out, "known: ")
	if !ok {
		return false
	}
	list, _, _ := strings.Cut(after, ")")
	for _, n := range strings.Split(list, ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
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

// The `build` verb is not covered here: it shells out to docker, and this box has
// none. It is exercised on the HOST by run_e2e.sh, which builds this very box
// with it under `set -e`.

// --- up / ls -----------------------------------------------------------------

func TestUpPrintsInstance(t *testing.T) {
	clean(t)
	out, code := run("dabs recipe " + baseDir + " --detach")
	wantExit(t, 0, code)
	wantContains(t, out, sandboxName+"-")
	wantContains(t, out, "recipe booted:")
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

// dabs's own flags end at the first bare `--`: everything after it is the
// appended command, verbatim. The flag scan used to keep reading past it and eat
// the command's OWN `--detach`/`--worktree` tokens — `recipe sh -- mytool
// --worktree x` silently lost two of mytool's arguments to dabs, and a trailing
// `--detach` flipped the whole run into a detached boot.
func TestRecipeDashDashShieldsTheCommandsOwnFlags(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-dashdash")
	bugRecipe(t, dir, "echoer", "")

	// `--detach` after `--` is the command's: sh -c 'echo tok-ok' --detach runs
	// (the token lands in $0, unused) — it must NOT boot a detached box.
	out, code := runInStdin(dir, "y\n", "dabs recipe echoer -- -c 'echo tok-ok' --detach")
	wantExit(t, 0, code)
	wantContains(t, out, "tok-ok")
	if containsFold(out, "detached") || containsFold(out, "runs no command") {
		t.Fatalf("a --detach after -- was eaten by dabs instead of reaching the command:\n%s", out)
	}

	// A trailing `--worktree` after `--` is the command's too — it used to error
	// with "--worktree needs a worktree name" without ever running anything.
	out, code = runInStdin(dir, "y\n", "dabs recipe echoer -- -c 'echo wt-ok' --worktree")
	wantExit(t, 0, code)
	wantContains(t, out, "wt-ok")

	// And before the `--`, flags are still dabs's: --detach boots, runs nothing.
	out, code = runInStdin(dir, "", "dabs recipe echoer --detach")
	wantExit(t, 0, code)
	wantContains(t, out, "instance:")
}

// Bringing a box down with --keep, when its spaces are empty, takes the box node
// too — an empty record is cruft, not history, so it does not linger as a `gone`
// row. What must not survive is a LIVE box; and the empty record must not survive
// either, in default `ls` OR in `ls --inactive`.
func TestLsAfterReapShowsNoLiveBox(t *testing.T) {
	clean(t)
	i := up(t)
	run("dabs rm " + i + " --keep --yes")
	out, _ := run("dabs ls")
	if isLive(out, i) {
		t.Fatalf("%s still live after down:\n%s", i, out)
	}
	// The empty box record is gone from the default listing...
	wantNotContains(t, out, i)
	// ...and it did not fall into the inactive bucket either — it was removed.
	inactive, _ := run("dabs ls --inactive")
	wantNotContains(t, inactive, i)
}

// isLive reports whether ls shows this instance as anything other than gone. The
// driver names the state (running, ready, …), so the test asks the only question
// it actually cares about.
func isLive(ls, instance string) bool {
	for _, line := range strings.Split(ls, "\n") {
		if strings.Contains(line, instance) && !strings.Contains(line, "gone") {
			return true
		}
	}
	return false
}

func TestLsShowsInstanceAndDriver(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs ls")
	wantContains(t, out, i)
}

// --- exec ---------------------------------------------------------------------

func TestRunEnvAndWorkdir(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs exec " + i +
		" -- sh -c 'echo E2E=$E2E cwd=$(pwd); cat /work/marker.txt'")
	wantContains(t, out, "E2E=yes")
	wantContains(t, out, "cwd=/work")
	wantContains(t, out, "hello-from-image")
}

func TestRunPrefixResolves(t *testing.T) {
	clean(t)
	i := up(t)
	prefix := i[:len(i)-6] // drop 6 hex chars; unique with a single instance
	out, _ := run("dabs exec " + prefix + " -- echo prefix-ok")
	wantContains(t, out, "prefix-ok")
}

func TestRunAmbiguous(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	out, _ := run("dabs exec " + sandboxName + " -- echo x")
	wantContains(t, out, "ambiguous")
}

func TestRunMissing(t *testing.T) {
	out, _ := run("dabs exec nope-missing -- echo x")
	wantContains(t, out, "no box matches")
}

func TestRunIsolationBetweenInstances(t *testing.T) {
	clean(t)
	a, b := up(t), up(t)
	run("dabs exec " + a + " -- touch /work/only-in-a")
	out, _ := run("dabs exec " + b + " -- ls /work")
	wantNotContains(t, out, "only-in-a")
}

func TestRunPersistenceWithinInstance(t *testing.T) {
	clean(t)
	i := up(t)
	run("dabs exec " + i + " -- touch /work/persisted")
	out, _ := run("dabs exec " + i + " -- ls /work")
	wantContains(t, out, "persisted")
}

// TestRunShellWraps proves `dabs exec` without a `--` is the friendly level: it
// runs a shell command LINE (via sh -c), so a pipe works without the caller
// writing sh -c or a `--` separator — the command reaches the box as one string.
func TestRunShellWraps(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs exec " + i + " 'echo hi | tr a-z A-Z'")
	wantContains(t, out, "HI")
}

// TestRunExitCodePropagates (B1): a box command that exits non-zero is the box
// command's failure, not dabs's. `dabs exec` must mirror that exit code and
// NOT print a spurious `dabs: bwrap:` (or `dabs: apple:`) wrapper line, while a
// real driver failure (no such instance) still reports clearly as a dabs error.
func TestRunExitCodePropagates(t *testing.T) {
	clean(t)
	i := up(t)

	out, code := run("dabs exec " + i + " 'exit 7'")
	wantExit(t, 7, code)
	wantNotContains(t, out, "dabs: bwrap:")
	wantNotContains(t, out, "dabs: apple:")
	wantNotContains(t, out, "run in ")

	out, code = run("dabs exec " + i + " -- sh -c 'exit 3'")
	wantExit(t, 3, code)
	wantNotContains(t, out, "dabs: bwrap:")
	wantNotContains(t, out, "dabs: apple:")

	// A real failure — no such instance — is still a clear dabs error, not exit 0.
	out, code = run("dabs exec nope-missing -- echo x")
	if code == 0 {
		t.Fatalf("missing instance should be a dabs error, got exit 0:\n%s", out)
	}
	wantContains(t, out, "no box matches")
}

// --- rm (the single reaper; it absorbed down) --------------------------------

func TestRmStopsBox(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs rm " + i + " --yes")
	wantContains(t, out, i+" stopped")
}

func TestRmDryListsAndKeeps(t *testing.T) {
	clean(t)
	a, b := up(t), up(t)
	out, _ := run("dabs rm " + sandboxName + " --multiple --dry")
	wantContains(t, out, "reaps")
	ls, _ := run("dabs ls")
	for _, i := range []string{a, b} {
		if !isLive(ls, i) {
			t.Fatalf("--dry reaped %s; it must only preview:\n%s", i, ls)
		}
	}
}

// CONTRACT (the multi-match guard): a name matching MORE THAN ONE box is NOT
// reaped by --yes alone — yes only skips the consent prompt. Acting on several
// boxes takes the explicit --multiple. An over-broad name must never quietly
// wipe a fleet; it must refuse and leave everything standing.
func TestRmRefusesMultiMatchWithoutMultiple(t *testing.T) {
	clean(t)
	a, b := up(t), up(t)
	// --yes alone against a prefix matching both boxes: reaps NOTHING.
	run("dabs rm " + sandboxName + " --yes")
	out, _ := run("dabs ls")
	for _, i := range []string{a, b} {
		if !isLive(out, i) {
			t.Fatalf("--yes alone reaped %s on a multi-match; only --multiple may:\n%s", i, out)
		}
	}

	// --multiple is the approval — now they go.
	run("dabs rm " + sandboxName + " --multiple --yes")
	out, _ = run("dabs ls")
	for _, i := range []string{a, b} {
		if isLive(out, i) {
			t.Fatalf("%s still live after --multiple --yes:\n%s", i, out)
		}
	}
}

func TestRmMissingIsNotError(t *testing.T) {
	out, code := run("dabs rm nope-missing")
	wantExit(t, 0, code)
	wantContains(t, out, "no node")
}

// --- logging a harness in ------------------------------------------------------

// CONTRACT: there is no login verb. A recipe mkmounts a shared login dir; the
// FIRST box creates it (mkmount, not mount — the dir is not there yet), the
// harness logs in inside the box, and the credential lands on the HOST because
// the mount is live. Every LATER box that names the same dir is already logged
// in. This is the whole mechanism, so it is tested as one story.
//
// The fake `claude` baked into the base image is a real CLI on PATH that only
// "logs in": it writes the credential Claude Code would and exits.
func TestSharedLoginDirIsCreatedCapturedAndReusedByEveryBox(t *testing.T) {
	clean(t)
	installRecipes(t)
	shared := filepath.Join(home, ".dabs", "shared", "claude")
	cred := filepath.Join(shared, ".credentials.json")

	// It does not exist yet. mkmount must create it; a plain mount would refuse.
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Fatalf("shared login dir already exists before any box ran: %v", err)
	}

	dir := filepath.Join(home, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// First box: creates the dir, logs in, credential writes through to the host.
	out, code := runIn(dir, "dabs recipe claude-mounted")
	wantExit(t, 0, code)
	wantContains(t, out, "fake-claude: login ok")

	data, err := os.ReadFile(cred)
	if err != nil {
		t.Fatalf("credential not captured to the host: %v", err)
	}
	wantContains(t, string(data), "fake-token")

	// Second box: the login is already there, mounted from the same host dir.
	out2, code2 := runIn(dir, "dabs exec "+upRecipeBox(t, dir)+" cat /root/.claude/.credentials.json")
	wantExit(t, 0, code2)
	wantContains(t, out2, "fake-token")
}

// upRecipeBox boots a box from the shared-login recipe and returns its instance,
// so a test can look inside a box the recipe provisioned.
func upRecipeBox(t *testing.T, dir string) string {
	t.Helper()
	out, code := runIn(dir, "dabs recipe claude-mounted --detach")
	if code != 0 {
		t.Fatalf("up: %s", out)
	}
	// The instance is on the `id:` line (the leading line names the recipe).
	for _, line := range strings.Split(out, "\n") {
		if inst, ok := strings.CutPrefix(strings.TrimSpace(line), "id: "); ok {
			return strings.TrimSpace(inst)
		}
	}
	t.Fatalf("up printed no id line: %q", out)
	return ""
}

// --- recipes -----------------------------------------------------------------

// The e2e recipes: three made-up recipes that each place the code into /work a
// different way (mount / copy / worktree) and mkmount the shared login dir, then run the
// FAKE `claude` (which writes a credential and exits). They use the prebuilt
// base image (dabs-e2e) so nothing is built in-box. Written to the user's
// ~/.dabs/recipes.yaml, so this also exercises user-registry override + merge.
const e2eRecipes = `recipes:
  claude-mounted:
    image: dabs-e2e
    command: [sh, -c, "echo box-was-here > /work/from-box.txt; claude"]
    sources:
      - mkmount: ~/.dabs/shared/claude
        path: /root/.claude
      - mount: .
        path: /work
  claude-isolated:
    image: dabs-e2e
    command: [sh, -c, "cat /work/seed.txt; echo box > /work/from-box.txt; claude"]
    sources:
      - mkmount: ~/.dabs/shared/claude
        path: /root/.claude
      - copy: .
        path: /work
  claude-new-worktree:
    image: dabs-e2e
    command: [sh, -c, "echo box-was-here > /work/from-box.txt; claude"]
    sources:
      - mkmount: ~/.dabs/shared/claude
        path: /root/.claude
      - worktree: .
        path: /work
  shellhang:
    image: dabs-e2e
    command: [sh]
  # For --worktree: a worktree-source recipe whose command DOES git — proving the
  # bound worktree made the box git-capable. It commits (empty) and records the new
  # HEAD into /work (the live-mounted worktree), so the host can confirm the commit
  # reconciled.
  gitprobe:
    image: dabs-e2e
    command: [sh, -c, "cd /work && git rev-parse --abbrev-ref HEAD > BRANCH && git -c user.email=box@dabs.test -c user.name=box commit --allow-empty -qm 'from worktree box' && git rev-parse HEAD > CAST_HEAD"]
    sources:
      - worktree: .
        path: /work
`

// installRecipes writes the e2e recipes to ~/.dabs/recipes.yaml. The shared login
// dir they mkmount is NOT created here: creating it is the recipe's job, and that
// is under test.
func installRecipes(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".dabs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".dabs", "recipes.yaml"), []byte(e2eRecipes), 0o644); err != nil {
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
	data, err := os.ReadFile(filepath.Join(home, ".dabs", "shared", "claude", ".credentials.json"))
	if err != nil {
		t.Fatalf("vault mount failed: credential not written back to host: %v", err)
	}
	wantContains(t, string(data), "fake-token")
}

// TestRecipesLists proves `dabs recipes` shows the bundled `sh` recipe and
// the user's recipes together.
func TestRecipesLists(t *testing.T) {
	installRecipes(t)
	out, code := run("dabs recipes")
	wantExit(t, 0, code)
	if !hasRecipeLine(out, "sh") { // bundled (the only shipped recipe)
		t.Fatalf("sh recipe not listed on its own row:\n%s", out)
	}
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
	if !knownListsRecipe(out, "sh") { // the bundled recipe is always among the known ones
		t.Fatalf("sh not named as a distinct entry in the known list:\n%s", out)
	}
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
	wantContains(t, out, "kept:")

	// The box's write landed in a KEPT worktree, not the original repo.
	if _, err := os.Stat(filepath.Join(repo, "from-box.txt")); err == nil {
		t.Fatalf("worktree not isolated: box's write appeared in the original repo")
	}
	wts := worktreeDirs(t)
	if len(wts) == 0 {
		t.Fatalf("expected a kept worktree node; got %v", wts)
	}
	wt := filepath.Join(worktreeData(wts[0]), "from-box.txt")
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("box's write not found in the kept worktree: %v", err)
	}
	vaultHasToken(t)
}

// TestNonKeepWorktreeRecipeJournalBalanced (regression, finding #1): a NON-KEEP
// `worktree:` recipe (claude-new-worktree) tears its box down automatically when
// the command exits — the journal must record BOTH the `up` and a matching
// `down`, so the (now dead) box never reads as live. The worktree survives (kept)
// but `dabs worktrees` shows it with no box.
func TestNonKeepWorktreeRecipeJournalBalanced(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t) // isolate the journal too
	repo := filepath.Join(home, "wtbalance")
	gitRepo(t, repo)

	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	logPath := journalPath()
	log := readFile(t, logPath)
	t.Logf("log.jsonl after non-keep recipe:\n%s", log)
	if up, down := strings.Count(log, `"event":"up"`), strings.Count(log, `"event":"down"`); up != 1 || down != 1 {
		t.Fatalf("journal not balanced: up=%d down=%d\n%s", up, down, log)
	}

	// The kept worktree reads as having no live box (the box was torn down).
	out, code := run("dabs worktrees")
	wantExit(t, 0, code)
	t.Logf("dabs worktrees after non-keep teardown:\n%s", out)
	wantContains(t, out, "no box")
	if strings.Contains(out, " live") {
		t.Fatalf("a torn-down box still reads as live:\n%s", out)
	}
}

// TestWorktreesInspectAndGuardedReap: a recipe leaves a worktree with the box's
// uncommitted write; `dabs worktrees` lists it as having work, `rm` refuses to
// discard it, and `rm --force` reaps it.
func TestWorktreesInspectAndGuardedReap(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t) // isolate from other tests' kept worktrees
	repo := filepath.Join(home, "wtreap")
	gitRepo(t, repo)
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	wts := worktreeDirs(t)
	if len(wts) == 0 {
		t.Fatalf("no worktree created: %v", wts)
	}
	name := wts[0]

	out, code := run("dabs worktrees")
	wantExit(t, 0, code)
	wantContains(t, out, "has work") // the box wrote from-box.txt (uncommitted)

	out, code = run("dabs rm " + name + " -y")
	if code == 0 {
		t.Fatalf("rm removed a worktree with unreviewed work without --force\n%s", out)
	}
	wantContains(t, out, "unreviewed work")

	out, code = run("dabs rm " + name + " -y --force")
	wantExit(t, 0, code)
	wantContains(t, out, "removed")
	if e := worktreeDirs(t); len(e) != 0 {
		t.Fatalf("worktree not reaped after --force: %v", e)
	}
}

// TestWorktreesDiffShowsUntrackedFiles (B29): `git diff` is blind to untracked
// files, so a reviewer deciding merge-vs-discard would miss every net-new file an
// agent created — often its whole contribution. The diff must surface them. The
// claude-new-worktree recipe writes an untracked from-box.txt into the worktree.
func TestWorktreesDiffShowsUntrackedFiles(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t)
	repo := filepath.Join(home, "wtdiff")
	gitRepo(t, repo)
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	wts := worktreeDirs(t)
	if len(wts) == 0 {
		t.Fatalf("no worktree created: %v", wts)
	}
	name := wts[0]

	out, code := run("dabs worktrees diff " + name)
	wantExit(t, 0, code)
	wantContains(t, out, "from-box.txt") // the untracked net-new file must appear
	wantContains(t, out, "box-was-here") // and its contents
}

// TestRmWorktreeGuardsUnreviewedWork (finding B26/B27): the git-work guard is not
// the property of one verb. `dabs rm <worktree> -y` must honour it — the same guard
// `dabs rm --clean-worktrees` applies: -y consents to the held space, but discarding
// unreviewed git work needs the stronger --force. A childless worktree LEAF (no
// cascade) is the case B27 slipped through: it reaps with no prompt at all, so it
// is precisely where a plain `rm -y` would silently destroy work.
func TestRmWorktreeGuardsUnreviewedWork(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t)
	repo := filepath.Join(home, "wtrmguard")
	gitRepo(t, repo)
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree, got %v", wts)
	}
	name := wts[0]
	work := filepath.Join(worktreeData(name), "from-box.txt") // the box's uncommitted write

	// -y is NOT enough to discard git work: refused, work survives, points to --force.
	out, code := run("dabs rm " + name + " -y")
	if code == 0 {
		t.Fatalf("rm -y destroyed a worktree with unreviewed work without --force\n%s", out)
	}
	wantContains(t, out, "unreviewed work")
	wantContains(t, out, "--force")
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("rm -y lost the uncommitted work despite refusing: %v", err)
	}
	if e := worktreeDirs(t); len(e) != 1 {
		t.Fatalf("refused rm still reaped the node: %v", e)
	}

	// --force is the approval — the node reaps.
	out, code = run("dabs rm " + name + " -y --force")
	wantExit(t, 0, code)
	wantContains(t, out, "removed")
	if e := worktreeDirs(t); len(e) != 0 {
		t.Fatalf("worktree not reaped after --force: %v", e)
	}
}

// TestRmWorktreeDeregistersFromGit: reaping a worktree node must leave git
// clean — no prunable worktree registration and no orphan branch. The checkout
// lives in the node's held space, so the reap has to deregister the
// worktree from git BEFORE deleting that space, while git can still resolve the
// repo from the checkout.
func TestRmWorktreeDeregistersFromGit(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t)
	repo := filepath.Join(home, "wtgitcleanup")
	gitRepo(t, repo)
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree, got %v", wts)
	}
	name := wts[0]

	// Before: git registers the worktree and holds its branch.
	if list, _ := run("git -C " + repo + " worktree list"); !strings.Contains(list, "held/worktree") {
		t.Fatalf("git did not register the worktree:\n%s", list)
	}
	if b, _ := run("git -C " + repo + " branch --list dabs/*"); !strings.Contains(b, "dabs/") {
		t.Fatalf("no dabs/ branch after cutting a worktree:\n%s", b)
	}

	// Reap it (the box left uncommitted work, so --force is required).
	out, code := run("dabs rm " + name + " -y --force")
	wantExit(t, 0, code)
	wantContains(t, out, "removed")

	// After: no registration left behind (prunable or otherwise), no orphan branch.
	list, _ := run("git -C " + repo + " worktree list")
	if strings.Contains(list, "held/worktree") || strings.Contains(list, "prunable") {
		t.Fatalf("reap left a worktree registration behind:\n%s", list)
	}
	if b, _ := run("git -C " + repo + " branch --list dabs/*"); strings.TrimSpace(b) != "" {
		t.Fatalf("reap left an orphan branch:\n%s", b)
	}
}

// TestRmCleanWorktreeNeedsNoForce: the guard is about WORK, not about being a
// worktree. A clean worktree (nothing uncommitted, nothing ahead) reaps with -y.
func TestRmCleanWorktreeNeedsNoForce(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t)
	repo := filepath.Join(home, "wtrmclean")
	gitRepo(t, repo)
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("recipe failed")
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree, got %v", wts)
	}
	name := wts[0]
	// Drop the box's untracked write so the checkout is clean and not ahead.
	gitOut(t, worktreeData(name), "clean", "-fdx")
	ls, _ := run("dabs worktrees")
	if strings.Contains(ls, "has work") {
		t.Fatalf("worktree still reads as having work after clean:\n%s", ls)
	}

	out, code := run("dabs rm " + name + " -y")
	wantExit(t, 0, code)
	wantContains(t, out, "removed")
	if e := worktreeDirs(t); len(e) != 0 {
		t.Fatalf("clean worktree not reaped with -y: %v", e)
	}
}

// gitOut runs a git command in dir and returns trimmed stdout (fatal on error).
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestWorktreeFlagAttachesWorktreeAndGivesGit: `dabs recipe <recipe> --worktree
// <wt>` binds an EXISTING worktree to the recipe's `worktree: .` source — it
// mounts the worktree live (never forks a new branch) AND mounts the parent .git,
// so git works inside the box. Proven end-to-end: the box reads the branch,
// commits, and that commit reconciles into the shared store (visible from the repo).
func TestWorktreeFlagAttachesWorktreeAndGivesGit(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t) // isolate from other tests
	repo := filepath.Join(home, "castrepo")
	gitRepo(t, repo)

	// A prior agent left a worktree (created the dabs way).
	if _, code := runIn(repo, "dabs recipe claude-new-worktree"); code != 0 {
		t.Fatalf("seed worktree via recipe failed")
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want exactly one seeded worktree, got %v", wts)
	}
	name := wts[0]
	wt := worktreeData(name)

	// Bind a git-doing recipe onto that existing worktree.
	out, code := run("dabs recipe gitprobe --worktree " + name)
	wantExit(t, 0, code)
	wantContains(t, out, "mounting it instead") // attached, did not fork

	// It must NOT have created a second worktree.
	if e := worktreeDirs(t); len(e) != 1 {
		t.Fatalf("--worktree forked a new worktree; want still one, got %v", e)
	}

	// git ran INSIDE the box against the worktree's own branch...
	branch := gitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD")
	gotBranch, err := os.ReadFile(filepath.Join(wt, "BRANCH"))
	if err != nil {
		t.Fatalf("box did not write BRANCH — git was blind in-box: %v", err)
	}
	if strings.TrimSpace(string(gotBranch)) != branch {
		t.Fatalf("in-box branch %q != worktree branch %q", strings.TrimSpace(string(gotBranch)), branch)
	}

	// ...and its commit reconciled into the SHARED store: the sha the box wrote
	// is a real commit object reachable from the original repo, subject and all.
	shaBytes, err := os.ReadFile(filepath.Join(wt, "CAST_HEAD"))
	if err != nil {
		t.Fatalf("box could not commit — no CAST_HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))
	if typ := gitOut(t, repo, "cat-file", "-t", sha); typ != "commit" {
		t.Fatalf("box commit %s not in shared store from the repo (type %q)", sha, typ)
	}
	if subj := gitOut(t, wt, "log", "-1", "--format=%s"); subj != "from worktree box" {
		t.Fatalf("worktree HEAD subject = %q, want 'from worktree box'", subj)
	}
}

// instanceFrom pulls the base-box driver instance name out of `dabs recipe
// --detach` output — the value on the `instance:` line. The NODE ID shares the
// recipe's `dabs-e2e-` prefix, so scanning for the prefix is ambiguous; the
// labelled line is not.
func instanceFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if inst, ok := strings.CutPrefix(strings.TrimSpace(line), "instance: "); ok {
			return strings.TrimSpace(inst)
		}
	}
	t.Fatalf("no instance line in output:\n%s", out)
	return ""
}

// TestWorktreeBoxLifecycleLog drives the append-only journal end to end: bring
// up a worktree-backed box (`dabs recipe --detach` on a `worktree:` recipe), confirm
// log.jsonl gains an `up` and `dabs worktrees` shows the box live under the
// worktree's absolute path, then `dabs rm --keep` and confirm a `down` entry and that
// the worktree reads as having no box. The log is the sole instance→worktree
// record; nothing else knows the box was worktree-backed.
func TestWorktreeBoxLifecycleLog(t *testing.T) {
	clean(t)
	installRecipes(t)
	resetNodes(t) // isolate from other tests' worktrees AND their log
	repo := filepath.Join(home, "wtlogrepo")
	gitRepo(t, repo)

	// Bring up a worktree-backed box detached (--detach runs no command, keeps the box).
	out, code := runIn(repo, "dabs recipe claude-new-worktree --detach")
	wantExit(t, 0, code)
	wantContains(t, out, "kept:")
	inst := instanceFrom(t, out)
	t.Cleanup(func() { run("dabs rm " + inst + " --yes") })

	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want exactly one worktree, got %v", wts)
	}
	wtName := wts[0]
	wtPath := worktreeData(wtName)
	logPath := journalPath()

	// The `up` entry links the box to the worktree by name and absolute path.
	logAfterUp := readFile(t, logPath)
	t.Logf("log.jsonl after up:\n%s", logAfterUp)
	if n := strings.Count(logAfterUp, `"event":"up"`); n != 1 {
		t.Fatalf("want one up entry, log is:\n%s", logAfterUp)
	}
	for _, want := range []string{`"event":"up"`, `"instance":"` + inst + `"`, `"worktree":"` + wtName + `"`, `"path":"` + wtPath + `"`} {
		wantContains(t, logAfterUp, want)
	}
	if strings.Contains(logAfterUp, `"event":"down"`) {
		t.Fatalf("no down expected yet, log is:\n%s", logAfterUp)
	}

	// `dabs worktrees` shows NAME | WORKTREE(abs path) | STATE | DETAIL with the
	// box reported live from the log.
	out, code = run("dabs worktrees")
	wantExit(t, 0, code)
	t.Logf("dabs worktrees (box up):\n%s", out)
	for _, want := range []string{"NAME", "WORKTREE", "STATE", "DETAIL", wtPath, "box " + inst + " live"} {
		wantContains(t, out, want)
	}

	// Stop the box (keep its record) — the journal gains a matching `down`.
	out, code = run("dabs rm " + inst + " --keep --yes")
	wantExit(t, 0, code)
	logAfterDown := readFile(t, logPath)
	t.Logf("log.jsonl after down:\n%s", logAfterDown)
	if n := strings.Count(logAfterDown, `"event":"down"`); n != 1 {
		t.Fatalf("want one down entry after down, log is:\n%s", logAfterDown)
	}
	wantContains(t, logAfterDown, `"event":"down"`)

	// With the box down, the worktree reads as having no live box.
	out, code = run("dabs worktrees")
	wantExit(t, 0, code)
	t.Logf("dabs worktrees (box down):\n%s", out)
	wantContains(t, out, "no box")
	if strings.Contains(out, "box "+inst+" live") {
		t.Fatalf("worktree still shows a live box after down:\n%s", out)
	}
}

// worktreeDirs lists the WORKTREE nodes. Every kind of node lives under
// ~/.dabs/nodes — project, workdir, worktree, box — so the kind in each node's
// record is what tells them apart, never the directory listing.
func worktreeDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, ".dabs", "nodes"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read nodes dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(home, ".dabs", "nodes", e.Name(), "dabs-node.json"))
		if err != nil {
			continue
		}
		var n struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(b, &n) == nil && n.Kind == "worktree" {
			out = append(out, e.Name())
		}
	}
	return out
}

// worktreeData is the checkout a worktree node owns — what a recipe's `.` sees.
func worktreeData(name string) string {
	return filepath.Join(home, ".dabs", "nodes", name, "held", "worktree")
}

// journalPath is the box-lifecycle journal.
func journalPath() string { return filepath.Join(home, ".dabs", "log.jsonl") }

// resetNodes clears everything dabs provisioned plus the journal, isolating a
// test from what other tests left behind.
func resetNodes(t *testing.T) {
	t.Helper()
	os.RemoveAll(filepath.Join(home, ".dabs", "nodes"))
	os.Remove(journalPath())
}

// --- Bundled recipes: sh / wt / wtbox / scratch / scratchbox ------------------
// Each must work on an installed-only dabs: a directory with NO dabs.yaml and
// NO ~/.dabs/recipes.yaml, served by the BUNDLED registry alone.

// bundledOnly strips every non-bundled registry, so the recipe under test can
// only have come from the binary itself. In the suite's own box it also
// restores the staged `shell` image from the box's pristine copy: the box has
// no builder, so a `dabs prune` run by an earlier test would otherwise leave
// the bundled box recipes nothing to boot from. On a CI runner there is no
// pristine copy — and a builder, so a missing image simply builds.
func bundledOnly(t *testing.T) {
	t.Helper()
	resetNodes(t)
	userRecipes := filepath.Join(home, ".dabs", "recipes.yaml")
	os.Remove(userRecipes)
	t.Cleanup(func() { os.Remove(userRecipes) })

	staged := filepath.Join(home, ".dabs-staged-images", "shell")
	dest := filepath.Join(home, ".dabs", "images", "shell")
	if _, err := os.Stat(staged); err != nil {
		return // no pristine copy here — this environment builds instead
	}
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("cp", "-a", staged, dest).CombinedOutput(); err != nil {
			t.Fatalf("restore staged shell image: %v\n%s", err, out)
		}
	}
}

// bootBundled boots a bundled box recipe detached from dir and returns its
// instance name, reaping the box when the test ends.
func bootBundled(t *testing.T, dir, name string) string {
	t.Helper()
	out, code := runIn(dir, "dabs recipe "+name+" --detach")
	if code != 0 {
		t.Fatalf("bundled recipe %s --detach failed (%d): %s", name, code, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if inst, ok := strings.CutPrefix(strings.TrimSpace(line), "instance: "); ok {
			inst = strings.TrimSpace(inst)
			t.Cleanup(func() { run("dabs rm " + inst + " --yes") })
			return inst
		}
	}
	t.Fatalf("recipe %s --detach printed no instance line: %q", name, out)
	return ""
}

// CONTRACT: `wt` ships BUNDLED — `cd my/project && dabs recipe wt` cuts a
// worktree. It makes a place, no box: the checkout lands in the node's held
// space, so `rm` asks before reaping it.
func TestBundledWtRecipeWorksWithoutAnyRegistry(t *testing.T) {
	bundledOnly(t)
	repo := filepath.Join(home, "e2e-bundled-wt")
	gitRepo(t, repo) // deliberately NO dabs.yaml in it

	if out, code := run("dabs recipes"); code != 0 || !strings.Contains(out, "wt") {
		t.Fatalf("`dabs recipes` must list the bundled wt (%d):\n%s", code, out)
	}
	if out, code := runIn(repo, "dabs recipe wt"); code != 0 {
		t.Fatalf("bundled recipe wt failed (%d): %s", code, out)
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree node, got %v", wts)
	}
	// The checkout is a real worktree of the repo, in the node's HELD space.
	if _, err := os.Stat(filepath.Join(worktreeData(wts[0]), "tracked.txt")); err != nil {
		t.Fatalf("worktree checkout missing the repo's file: %v", err)
	}
}

// CONTRACT: `wtbox` ships BUNDLED — a shell box over a FRESH worktree. The box
// sees the repo's files at /work; the repo's own tree is untouched by in-box
// writes (they land in the worktree).
func TestBundledWtboxRecipeWorksWithoutAnyRegistry(t *testing.T) {
	bundledOnly(t)
	repo := filepath.Join(home, "e2e-bundled-wtbox")
	gitRepo(t, repo)

	inst := bootBundled(t, repo, "wtbox")
	out, code := run("dabs exec " + inst + " -- cat /work/tracked.txt")
	if code != 0 || !strings.Contains(out, "v1") {
		t.Fatalf("box does not see the worktree checkout at /work (%d): %s", code, out)
	}
	if out, code := run("dabs exec " + inst + " 'echo boxed > /work/boxed.txt'"); code != 0 {
		t.Fatalf("write in box failed (%d): %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "boxed.txt")); !os.IsNotExist(err) {
		t.Fatalf("in-box write must land in the worktree, not the repo's own tree (err=%v)", err)
	}
	wts := worktreeDirs(t)
	if len(wts) != 1 {
		t.Fatalf("want one worktree node, got %v", wts)
	}
	if _, err := os.Stat(filepath.Join(worktreeData(wts[0]), "boxed.txt")); err != nil {
		t.Fatalf("in-box write not in the worktree checkout: %v", err)
	}
}

// CONTRACT: `scratch` ships BUNDLED — copy the cwd into a directory node and
// stop; no box, and NO git needed. The copy lands in the node's held space.
func TestBundledScratchRecipeWorksWithoutAnyRegistry(t *testing.T) {
	bundledOnly(t)
	dir := filepath.Join(home, "e2e-bundled-scratch") // a plain dir, not a repo
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("s1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, code := runIn(dir, "dabs recipe scratch"); code != 0 {
		t.Fatalf("bundled recipe scratch failed (%d): %s", code, out)
	}
	dirs := nodesOfKind(t, "workdir")
	if len(dirs) != 1 {
		t.Fatalf("want one workdir node, got %v", dirs)
	}
	copied := filepath.Join(nodesDir(), dirs[0], "held", "work", "seed.txt")
	b, err := os.ReadFile(copied)
	if err != nil || string(b) != "s1\n" {
		t.Fatalf("copy missing from the node's held space (%v): %q", err, b)
	}
	// A place, no box: a write to the copy must not appear in the origin dir.
	if err := os.WriteFile(filepath.Join(nodesDir(), dirs[0], "held", "work", "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("scratch must be a COPY — write leaked to the origin dir (err=%v)", err)
	}
}

// CONTRACT: `scratchbox` ships BUNDLED — a shell box over a throwaway COPY of
// the cwd; no git needed, the host is never written.
func TestBundledScratchboxRecipeWorksWithoutAnyRegistry(t *testing.T) {
	bundledOnly(t)
	dir := filepath.Join(home, "e2e-bundled-scratchbox") // a plain dir, not a repo
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("s1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := bootBundled(t, dir, "scratchbox")
	out, code := run("dabs exec " + inst + " -- cat /work/seed.txt")
	if code != 0 || !strings.Contains(out, "s1") {
		t.Fatalf("box does not see the copy at /work (%d): %s", code, out)
	}
	if out, code := run("dabs exec " + inst + " 'echo boxed > /work/boxed.txt'"); code != 0 {
		t.Fatalf("write in box failed (%d): %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "boxed.txt")); !os.IsNotExist(err) {
		t.Fatalf("scratchbox must be a COPY — in-box write reached the host dir (err=%v)", err)
	}
}

// readFile returns a file's contents (fatal on error), for asserting on the log.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
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
	// The default marker is a lipgloss badge that degrades to a bare word when
	// piped; assert it sits next to the recipe name so a dropped/misattached
	// marker still fails.
	wantContains(t, out, "probe default")

	// No name → the default-recipe path, which confirms before running: feed `y`.
	out, code = runInStdin(dir, "y\n", "dabs recipe")
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

// TestUpUnknownRecipeLists: `dabs recipe <bogus> --detach` (not a recipe, not a
// path) fails clearly, listing the known recipes and pointing at the
// recipe/path/default forms — build/detach resolve a recipe now, not a manifest.
func TestUpUnknownRecipeLists(t *testing.T) {
	out, code := run("dabs recipe not-a-recipe --detach")
	if code == 0 {
		t.Fatalf("want non-zero exit; got 0\n%s", out)
	}
	wantContains(t, out, "no recipe")
	wantContains(t, out, "dabs.yaml path") // the hint naming the accepted forms
}

// TestUpFromDabsYamlPath: `dabs recipe <path/to/dabs.yaml> --detach` loads that
// file and boots its recipe — proving the "a path is a dabs.yaml to load" form.
// The recipe reuses the prebuilt base image by BARE name, so detach needs no
// builder in-box.
func TestUpFromDabsYamlPath(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "yamlpath")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "default: " + sandboxName + "\nrecipes:\n  " + sandboxName +
		":\n    image: " + sandboxName + "\n    workdir: /work\n"
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run("dabs recipe " + filepath.Join(dir, "dabs.yaml") + " --detach") // a FILE path
	wantExit(t, 0, code)
	wantContains(t, out, sandboxName+"-")
	wantContains(t, out, "recipe booted:")
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
	// The fleet renders as a NAME/VIA/DESTINATION table, so the transport and
	// host land in separate columns (padding between them) rather than a
	// contiguous "ssh host1.example". Assert each meaningful token — name,
	// transport, host — so a missing/garbled value still fails.
	wantContains(t, out, "s1")
	wantContains(t, out, "ssh")
	wantContains(t, out, "host1.example")
}

func TestServersRemove(t *testing.T) {
	run("dabs servers add s2 host2")
	run("dabs servers rm s2")
	out, _ := run("dabs servers ls")
	wantNotContains(t, out, "s2")
}

// TestServersAddEmptyNameRejected: `dabs servers add ""` (an empty/whitespace
// name) is rejected with a non-zero exit and writes nothing, so it cannot poison
// config.json. The CLI must stay usable afterwards — a follow-up command exits 0.
func TestServersAddEmptyNameRejected(t *testing.T) {
	out, code := run("dabs servers add \"  \"")
	if code == 0 {
		t.Fatalf("want non-zero exit for empty server name; got 0\n%s", out)
	}
	wantContains(t, out, "empty")

	// Nothing was written, and the CLI is not bricked: --help still works, and
	// the fleet listing shows only the local machine.
	_, code = run("dabs --help")
	wantExit(t, 0, code)
	ls, code := run("dabs servers ls")
	wantExit(t, 0, code)
	wantContains(t, ls, "local")
}

// TestServersAddEmptyHostRejected: an explicit empty host is rejected, so a
// server can never be registered with an unusable destination.
func TestServersAddEmptyHostRejected(t *testing.T) {
	out, code := run("dabs servers add s3 \"  \"")
	if code == 0 {
		t.Fatalf("want non-zero exit for empty host; got 0\n%s", out)
	}
	wantContains(t, out, "empty host")
	ls, _ := run("dabs servers ls")
	wantNotContains(t, ls, "s3")
}

// TestServersAddRejectsReservedLocal: "local" is the built-in local driver's
// fleet key. Registering a server under it would shadow the built-in and
// misroute every local op to ssh, so `add local` is refused, writes nothing,
// and leaves exactly one local row — the built-in "this machine".
func TestServersAddRejectsReservedLocal(t *testing.T) {
	out, code := run("dabs servers add local")
	if code == 0 {
		t.Fatalf("want non-zero exit; got 0\n%s", out)
	}
	wantContains(t, out, "reserved")

	// Nothing written: the fleet still shows the built-in local, and only once.
	ls, _ := run("dabs servers ls")
	wantContains(t, ls, "this machine")
	if n := strings.Count(ls, "local"); n != 1 {
		t.Fatalf("want exactly one local row (the built-in); got %d\n%s", n, ls)
	}
}

// --- env marker --------------------------------------------------------------

func TestDabsNamePresentInBox(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := run("dabs exec " + i + " -- sh -c 'echo DABS_NAME=$DABS_NAME'")
	wantContains(t, out, "DABS_NAME="+i)
}

// A fresh machine that cannot fetch anything fails the first thing anyone
// does on a fresh machine. The bundled shell box carries curl (with CA
// certs); this drives the real box and asks it, no network required.
func TestShellBoxCarriesCurlE2E(t *testing.T) {
	clean(t)
	i := up(t)
	out, code := run("dabs exec " + i + " -- sh -c 'command -v curl && test -d /etc/ssl/certs && echo certs-ok'")
	wantExit(t, 0, code)
	wantContains(t, out, "curl")
	wantContains(t, out, "certs-ok")
}
