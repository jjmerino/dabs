//go:build e2e

// The two boot flags, end to end: `--no-command` boots a box and runs NOTHING,
// `--detach` boots one and STARTS the recipe's command in the background. Both
// are driven against the `longrunner` recipe — a command that keeps writing a
// line and never exits — so what the assertions read is the trace the command
// leaves (or does not), not an exit code.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localDriverKind boots a box the cheap way and reads back which driver ran it,
// so an assertion can name whatever driver this machine actually uses rather
// than a hardcoded guess. A refusal happens before any box exists, which is why
// the kind has to be learned separately.
func localDriverKind(t *testing.T) string {
	t.Helper()
	const probe = "e2e-kindprobe"
	defer run("dabs rm " + probe + " --yes")
	if out, code := run("dabs recipe longrunner --no-command --name " + probe); code != 0 {
		t.Fatalf("kind probe boot failed (%d): %s", code, out)
	}
	out, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("dabs ls failed (%d): %s", code, out)
	}
	for _, kind := range []string{"bwrap", "apple", "docker"} {
		if strings.Contains(out, "box ("+kind+")") {
			return kind
		}
	}
	t.Fatalf("no box kind found in:\n%s", out)
	return ""
}

// detachedLog is the host file a detached command's output goes to: the box
// node's own tmp space, plain filename, reaped with the node.
func detachedLog(node string) string {
	return filepath.Join(nodesDir(), node, "tmp", "detached.log")
}

// CONTRACT: `--no-command` leaves a box UP with the recipe's command NOT
// running. The box answers exec (it is up), the tick file the command would
// have written is absent (it never ran), and no detached log was made.
func TestNoCommandLeavesBoxUpWithCommandNotRunning(t *testing.T) {
	clean(t)
	installRecipes(t)
	const node = "e2e-nocmd"
	defer run("dabs rm " + node + " --yes")

	out, code := run("dabs recipe longrunner --no-command --name " + node)
	wantExit(t, 0, code)
	wantContains(t, out, "no command was run")

	// The box is UP: it answers an exec.
	if out, code := run("dabs exec " + node + " -- true"); code != 0 {
		t.Fatalf("the box should be up and answer exec (%d): %s", code, out)
	}
	// The command never ran: its tick file is absent.
	if out, code := run("dabs exec " + node + " -- ls /tmp/ticks"); code == 0 {
		t.Fatalf("--no-command ran the recipe's command; its tick file exists:\n%s", out)
	}
	if _, err := os.Stat(detachedLog(node)); err == nil {
		t.Fatalf("--no-command wrote a detached log at %s; nothing was started", detachedLog(node))
	}
}

// CONTRACT: `--detach` boots the box, STARTS the recipe's command in the
// background, and returns while the command is still running. The proofs: the
// boot returns although the command never exits; the command's output lands in
// the node's own log file on the HOST and keeps GROWING after the boot returned;
// and reaping the node takes the log with it.
//
// It needs a driver whose box carries a process of its own. Where the driver
// enters its box afresh per command, the command IS the box's life and cannot
// outlive the call that started it: `--detach` must REFUSE, naming the driver
// and the flag that does work there, and must leave nothing provisioned behind.
func TestDetachStartsTheCommandOrRefusesMechanically(t *testing.T) {
	clean(t)
	installRecipes(t)
	const node = "e2e-detach"
	defer run("dabs rm " + node + " --yes")

	// The recipe's command never exits, so a boot that WAITED for it would hang
	// here. The deadline is the proof that the call returned on its own.
	out, timedOut := runTimeout(30*time.Second, "dabs recipe longrunner --detach --name "+node)
	if timedOut {
		t.Fatalf("--detach waited on the command instead of returning:\n%s", out)
	}

	if strings.Contains(out, "cannot hold a detached command") {
		wantContains(t, out, "--no-command")
		// The refusal must name the driver that actually refused and carry ITS
		// reason. A wrapper that dropped the capability would refuse for every
		// driver, with a cause that is false for the ones that can detach.
		wantContains(t, out, "the "+localDriverKind(t)+" driver cannot hold a detached command")
		if lsOut, _ := run("dabs ls"); strings.Contains(lsOut, node) {
			t.Fatalf("a refused --detach left %s behind:\n%s", node, lsOut)
		}
		t.Skipf("this driver cannot hold a detached command; the refusal is the contract here:\n%s", out)
	}

	// The output line must point at the log, so nobody has to ask where it went.
	wantContains(t, out, "detached, running:")
	wantContains(t, out, detachedLog(node))

	// The command RAN: its output is in the node's log file on the host.
	first, err := os.ReadFile(detachedLog(node))
	if err != nil {
		t.Fatalf("no detached log at %s: %v", detachedLog(node), err)
	}
	if !strings.Contains(string(first), "tick") {
		t.Fatalf("the log holds no output from the command: %q", first)
	}
	// And it is STILL running: the log keeps growing after the boot returned.
	time.Sleep(3 * time.Second)
	second, err := os.ReadFile(detachedLog(node))
	if err != nil {
		t.Fatalf("re-reading the detached log failed: %v", err)
	}
	if len(second) <= len(first) {
		t.Fatalf("the detached command stopped writing: %d bytes then %d bytes", len(first), len(second))
	}

	// The log lives and dies with the node it belongs to.
	if out, code := run("dabs rm " + node + " --yes"); code != 0 {
		t.Fatalf("reaping the detached node failed (%d): %s", code, out)
	}
	if _, err := os.Stat(detachedLog(node)); !os.IsNotExist(err) {
		t.Fatalf("the detached log outlived its node (stat: %v)", err)
	}
}
