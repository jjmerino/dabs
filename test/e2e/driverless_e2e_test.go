//go:build e2e

// End-to-end proof that dabs works on a machine with NO sandbox driver at all.
// Each test runs the real `dabs` binary with a PATH cloned from the box's but
// holding no `bwrap` (and an isolated HOME, so a seeded node store cannot leak
// into the rest of the suite) — the genuine driverless-machine environment,
// not a fake. The contract:
//
//   - help/usage and the registry (`recipes`, `recipes --print <name>`) answer
//     from argv and YAML alone — exit 0, no driver involved;
//   - a BOXLESS recipe (`wt`, `scratch`) provisions its place — exit 0;
//   - `dabs ls` exits 0, warns exactly once on stderr with the driver's own
//     install hint, and keeps an UNCONFIRMED box (empty spaces, state
//     uncheckable) in the DEFAULT listing marked `(error: no driver)`;
//   - a box-booting recipe (`sh`) fails, with the install hint.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// driverlessEnv builds the environment of a machine with no sandbox driver:
// an isolated HOME, and a PATH of one directory holding a symlink to every
// executable the box's PATH offers EXCEPT bwrap. Everything else (git, cp, sh)
// keeps working — only the driver is gone. It returns the env and the absolute
// path of the dabs binary (resolved before the strip, since the clone skips
// nothing else).
func driverlessEnv(t *testing.T) (env []string, dabsBin string) {
	t.Helper()
	dabsBin, err := exec.LookPath("dabs")
	if err != nil {
		t.Fatalf("dabs not on PATH: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Name() == "bwrap" {
				continue
			}
			// First PATH dir wins, like real PATH resolution; a duplicate
			// name later on the PATH loses either way.
			_ = os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(bin, e.Name()))
		}
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + bin, "HOME=" + home}, dabsBin
}

// homeOf digs the isolated HOME back out of a driverlessEnv environment.
func homeOf(env []string) string {
	for _, kv := range env {
		if h, ok := strings.CutPrefix(kv, "HOME="); ok {
			return h
		}
	}
	return ""
}

// runDriverless runs one dabs command in the driverless environment, from dir
// (empty: any cwd), stdout and stderr kept separate — the warn-once contract
// is about which stream carries what.
func runDriverless(env []string, dabsBin, dir string, args ...string) (stdout, stderr string, code int) {
	cmd := exec.Command(dabsBin, args...)
	cmd.Env = env
	cmd.Dir = dir
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	_ = cmd.Run()
	return out.String(), errb.String(), cmd.ProcessState.ExitCode()
}

func TestDriverlessHelpAndRegistry(t *testing.T) {
	env, dabs := driverlessEnv(t)
	for _, args := range [][]string{{"--help"}, {"help"}, {"recipes"}, {"recipes", "--print", "sh"}} {
		stdout, stderr, code := runDriverless(env, dabs, "", args...)
		if code != 0 {
			t.Errorf("dabs %s on a driverless machine exited %d\nstdout: %s\nstderr: %s",
				strings.Join(args, " "), code, stdout, stderr)
		}
	}
	stdout, _, _ := runDriverless(env, dabs, "", "recipes", "--print", "sh")
	if !strings.Contains(stdout, "mount: .") {
		t.Errorf("recipes --print sh should show the recipe's mounts, got:\n%s", stdout)
	}
}

func TestDriverlessBoxlessRecipesProvision(t *testing.T) {
	env, dabs := driverlessEnv(t)
	proj := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"git", "init", "-q", "."},
		{"git", "-c", "user.email=e@e", "-c", "user.name=e", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = proj
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", c, err, out)
		}
	}
	if stdout, stderr, code := runDriverless(env, dabs, proj, "recipe", "wt"); code != 0 {
		t.Errorf("boxless `recipe wt` exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stdout, stderr, code := runDriverless(env, dabs, proj, "recipe", "scratch"); code != 0 {
		t.Errorf("boxless `recipe scratch` exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

func TestDriverlessLsWarnsOnceAndKeepsUnknownBox(t *testing.T) {
	env, dabs := driverlessEnv(t)
	// A box node with EMPTY spaces whose state cannot be checked: it must stay
	// in the DEFAULT listing (unconfirmed is not confirmed-dead), marked.
	node := filepath.Join(homeOf(env), ".dabs", "nodes", "ghostbox-e2e")
	if err := os.MkdirAll(node, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"id":"ghostbox-e2e","kind":"box","instance":"ghost-inst","recipe":"r","created":"t"}`
	if err := os.WriteFile(filepath.Join(node, "dabs-node.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runDriverless(env, dabs, "", "ls")
	if code != 0 {
		t.Fatalf("ls exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := strings.Count(stderr, "unavailable"); got != 1 {
		t.Errorf("ls should warn exactly once on stderr, warned %d times:\n%s", got, stderr)
	}
	if !strings.Contains(stderr, "bwrap") {
		t.Errorf("the warning should carry the driver's own message, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "ghostbox-e2e") {
		t.Errorf("unconfirmed box missing from the DEFAULT listing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "(error: no driver)") {
		t.Errorf("unconfirmed box should be marked `(error: no driver)`:\n%s", stdout)
	}
}

func TestDriverlessBoxRecipeFailsWithInstallHint(t *testing.T) {
	env, dabs := driverlessEnv(t)
	stdout, stderr, code := runDriverless(env, dabs, "", "recipe", "sh")
	if code == 0 {
		t.Fatalf("box-booting `recipe sh` succeeded with no driver\nstdout: %s", stdout)
	}
	if all := stdout + stderr; !strings.Contains(all, "'bwrap' not found") || !strings.Contains(all, "install") {
		t.Errorf("failure should carry the driver's install hint, got:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}
