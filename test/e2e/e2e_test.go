//go:build e2e

// End-to-end tests that drive the real `dabs` CLI (found on PATH, or via
// $DABS_BIN) — dabs is treated as an installed binary, not imported as a
// library. The dab under test is built from the current source at the start
// of the run, and the boxes it creates are exercised in place — dabs picks
// the platform's driver, the suite never does. Isolation is an isolated
// $HOME (a fresh ~/.dabs for config/images/instances, removed on teardown)
// plus unique per-box names, so runs never collide and assertions only ever
// concern this suite's own "dabs-e2e-*" boxes — other boxes on the machine
// are ignored.
//
// Run:  go test -tags e2e ./test/e2e
// Needs: dabs on PATH and its local-driver tools (dabs chooses the driver
// by platform: Apple `container` on macOS, bwrap + docker on Linux).
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	dabsBin string // the CLI under test, from $DABS_BIN or "dabs"
	home    string // isolated HOME for this run
	baseDir string // this package dir: holds the base image dabs.json + Dockerfile
)

const sandboxName = "dabs-e2e"

// --- setup / teardown --------------------------------------------------------

func TestMain(m *testing.M) { os.Exit(setupAndRun(m)) }

func setupAndRun(m *testing.M) int {
	var err error
	home, err = os.MkdirTemp("", "dabs-e2e-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	defer os.RemoveAll(home) // teardown: isolated HOME and everything under it
	os.Setenv("HOME", home)

	// The dab UNDER TEST is the CURRENT source: build it here so an e2e run
	// exercises the change with the least latency (an incremental `go build`
	// against a warm cache — no image rebuild). $DABS_BIN overrides, e.g. to
	// point at an already-built/stable binary. The OUTER sandbox that hosts
	// this run is a stable, cached environment and is never the thing tested.
	dabsBin = os.Getenv("DABS_BIN")
	if dabsBin == "" {
		dabsBin = filepath.Join(home, "dabs")
		if out, err := buildFromSource(dabsBin); err != nil {
			fmt.Fprintf(os.Stderr, "setup: build dabs from source: %v\n%s\n", err, out)
			return 1
		}
	}

	// The base image the inner boxes come from is this package's own
	// dabs.json + Dockerfile (name "dabs-e2e"); build it once with the source
	// dabs and reuse it across tests.
	baseDir, err = os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	if out, code := runDabs("build", baseDir); code != 0 {
		fmt.Fprintf(os.Stderr, "setup: dabs build base failed:\n%s\n", out)
		return 1
	}
	return m.Run()
}

// buildFromSource compiles the dabs binary under test from the repo source
// (two dirs up from this test package) to out.
func buildFromSource(out string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	repo, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = repo
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// --- helpers -----------------------------------------------------------------

func runDabs(args ...string) (string, int) {
	cmd := exec.Command(dabsBin, args...)
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

func dabs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return runDabs(args...)
}

func dabsStdin(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(dabsBin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, _ := cmd.CombinedOutput()
	return string(out)
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
	runDabs("down", sandboxName, "--force")
	t.Cleanup(func() { runDabs("down", sandboxName, "--force") })
}

// up starts a fresh base instance and returns its full name.
func up(t *testing.T) string {
	t.Helper()
	out, code := dabs(t, "up", baseDir)
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
	out, code := dabs(t)
	wantExit(t, 2, code)
	wantContains(t, out, "usage: dabs")
}

func TestUnknownCommand(t *testing.T) {
	out, code := dabs(t, "bogus")
	wantExit(t, 2, code)
	wantContains(t, out, "unknown command")
}

// --- build -------------------------------------------------------------------

func TestBuild(t *testing.T) {
	d := filepath.Join(home, "bt")
	writeManifest(d, "bt", "FROM alpine:3.20\nWORKDIR /work\n")
	out, code := dabs(t, "build", d)
	wantExit(t, 0, code)
	wantContains(t, out, "bt built")
}

// --- up / ls -----------------------------------------------------------------

func TestUpPrintsInstance(t *testing.T) {
	clean(t)
	out, code := dabs(t, "up", baseDir)
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
	out, _ := dabs(t, "ls")
	wantContains(t, out, a)
	wantContains(t, out, b)
}

func TestLsEmpty(t *testing.T) {
	clean(t)
	out, _ := dabs(t, "ls")
	wantContains(t, out, "local")
	wantContains(t, out, "this machine")
	wantNotContains(t, out, sandboxName+"-") // none of OUR instances
}

func TestLsShowsInstanceAndDriver(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := dabs(t, "ls")
	wantContains(t, out, i)
}

// --- run ---------------------------------------------------------------------

func TestRunEnvAndWorkdir(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := dabs(t, "run", i, "--", "sh", "-c", "echo E2E=$E2E cwd=$(pwd); cat /work/marker.txt")
	wantContains(t, out, "E2E=yes")
	wantContains(t, out, "cwd=/work")
	wantContains(t, out, "hello-from-image")
}

func TestRunPrefixResolves(t *testing.T) {
	clean(t)
	i := up(t)
	prefix := i[:len(i)-6] // drop 6 hex chars; unique with a single instance
	out, _ := dabs(t, "run", prefix, "--", "echo", "prefix-ok")
	wantContains(t, out, "prefix-ok")
}

func TestRunAmbiguous(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	out, _ := dabs(t, "run", sandboxName, "--", "echo", "x")
	wantContains(t, out, "ambiguous")
}

func TestRunMissing(t *testing.T) {
	out, _ := dabs(t, "run", "nope-missing", "--", "echo", "x")
	wantContains(t, out, "no instance matches")
}

func TestRunIsolationBetweenInstances(t *testing.T) {
	clean(t)
	a, b := up(t), up(t)
	dabs(t, "run", a, "--", "touch", "/work/only-in-a")
	out, _ := dabs(t, "run", b, "--", "ls", "/work")
	wantNotContains(t, out, "only-in-a")
}

func TestRunPersistenceWithinInstance(t *testing.T) {
	clean(t)
	i := up(t)
	dabs(t, "run", i, "--", "touch", "/work/persisted")
	out, _ := dabs(t, "run", i, "--", "ls", "/work")
	wantContains(t, out, "persisted")
}

// --- down --------------------------------------------------------------------

func TestDownOne(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := dabs(t, "down", i)
	wantContains(t, out, i+" down")
}

func TestDownDryListsAndKeeps(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	out, _ := dabs(t, "down", sandboxName, "--dry")
	wantContains(t, out, "matches")
	ls, _ := dabs(t, "ls")
	if strings.Count(ls, sandboxName+"-") != 2 {
		t.Fatalf("expected 2 instances after --dry, got:\n%s", ls)
	}
}

func TestDownForceReapsAll(t *testing.T) {
	clean(t)
	up(t)
	up(t)
	dabs(t, "down", sandboxName, "--force")
	out, _ := dabs(t, "ls")
	wantNotContains(t, out, sandboxName+"-")
}

func TestDownMissingIsNotError(t *testing.T) {
	out, code := dabs(t, "down", "nope-missing")
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
	out := dabsStdin(t, req, "mcp", i)
	wantContains(t, out, `"name":"dabash"`)
	wantContains(t, out, "hello-from-image")
}

// --- servers -----------------------------------------------------------------

func TestServersEmptyShowsLocalOnly(t *testing.T) {
	out, _ := dabs(t, "servers", "ls")
	wantContains(t, out, "local")
	wantContains(t, out, "this machine")
}

func TestServersAddAndList(t *testing.T) {
	dabs(t, "servers", "add", "s1", "host1.example")
	t.Cleanup(func() { runDabs("servers", "rm", "s1") })
	out, _ := dabs(t, "servers", "ls")
	wantContains(t, out, "s1")
	wantContains(t, out, "ssh host1.example")
}

func TestServersRemove(t *testing.T) {
	dabs(t, "servers", "add", "s2", "host2")
	dabs(t, "servers", "rm", "s2")
	out, _ := dabs(t, "servers", "ls")
	wantNotContains(t, out, "s2")
}

// --- install / uninstall -----------------------------------------------------

func TestInstallBarePrintsInstructions(t *testing.T) {
	out, _ := dabs(t, "install")
	wantContains(t, out, "dabs install <harness>")
}

func TestInstallAndUninstallClaude(t *testing.T) {
	dabsStdin(t, "y\n", "install", "claude")
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/dabs/SKILL.md")); err != nil {
		t.Fatalf("claude skill not installed: %v", err)
	}
	dabsStdin(t, "y\n", "uninstall", "claude")
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/dabs")); !os.IsNotExist(err) {
		t.Fatalf("claude skill not removed")
	}
}

func TestInstallPi(t *testing.T) {
	dabsStdin(t, "y\n", "install", "pi")
	t.Cleanup(func() { dabsStdin(t, "y\n", "uninstall", "pi") })
	if _, err := os.Stat(filepath.Join(home, ".pi/extensions/dabash/index.ts")); err != nil {
		t.Fatalf("pi extension not installed: %v", err)
	}
}

// --- env marker --------------------------------------------------------------

func TestDabsNamePresentInBox(t *testing.T) {
	clean(t)
	i := up(t)
	out, _ := dabs(t, "run", i, "--", "sh", "-c", "echo DABS_NAME=$DABS_NAME")
	wantContains(t, out, "DABS_NAME="+i)
}
