package actions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jjmerino/dabs/core/door"
	"github.com/jjmerino/dabs/core/proxy"
)

// The policy around the box door: where a box's door socket lives, who is
// allowed one, and the lifetime of the host-side relay that answers it. The
// protocol and the relay itself are core/door; what dabs does with them is
// here.

// doorFileName is the box node's door socket, in its tmp space. The host relay
// listens there and the box dials it; nothing else on the host uses it, and it
// is reaped with the node like any other scratch.
const doorFileName = "door.sock"

// doorLogFileName is where a box's relay writes what it did — a debugging
// trail beside the socket it serves.
const doorLogFileName = "door.log"

// doorReady is how long a boot waits for a freshly started relay to be
// listening. The box is booted next, and a driver that carries a socket into a
// box needs the socket to exist before the box does.
const doorReady = 5 * time.Second

// startRelay starts a box's door relay and returns its pid. It is a field on
// Real so a test can drive the boot without spawning a process; the default
// spawns one (spawnRelay). dir is the registry a box that may publish gets
// ("" for one that may not); egress is the proxy engine's socket the relay
// couples EGRESS crossings to ("" for a box with no proxy egress); carries
// are the recipe's host sockets, one relay listener each.
type startRelay func(doorPath, dir, logPath, egress string, carries []door.Carry) (int, error)

// resolveDoorPath returns the host path of a box node's door socket.
func (r Real) resolveDoorPath(nodeID string) (string, error) {
	tmp, err := r.resolveNodeSpace(nodeID, SpaceTmp)
	if err != nil {
		return "", err
	}
	return filepath.Join(tmp, doorFileName), nil
}

// resolveDoorLog returns the host path of a box node's relay log.
func (r Real) resolveDoorLog(nodeID string) (string, error) {
	tmp, err := r.resolveNodeSpace(nodeID, SpaceTmp)
	if err != nil {
		return "", err
	}
	return filepath.Join(tmp, doorLogFileName), nil
}

// spawnRelay starts a relay for one box as a detached child of this dabs, and
// returns once it is listening — the box is booted next, and a driver carries a
// socket into a box by binding a path that must already be there.
//
// The relay outlives the dabs process that started it, so its stdio must not be
// this one's: a child holding dabs's stderr keeps that pipe open and hangs
// whoever reads dabs's output. It logs to a file and leads its own session, so
// reaping its pid takes the whole thing.
func spawnRelay(doorPath, dir, logPath, egress string, carries []door.Carry) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("door relay: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return 0, fmt.Errorf("door relay: %w", err)
	}
	defer logFile.Close()
	args := []string{"services", "relay", "--door", doorPath}
	if dir != "" {
		args = append(args, "--dir", dir)
	}
	if egress != "" {
		args = append(args, "--egress", egress)
	}
	for _, c := range carries {
		args = append(args, "--carry", c.Listen+"="+c.Dial)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("door relay: %w", err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(doorReady)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(doorPath); err == nil {
			return pid, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	reapRelay(pid)
	// The relay's own log says what stopped it; a bare timeout says nothing.
	return 0, fmt.Errorf("door relay did not open %s: %s", doorPath, relayLogTail(logPath))
}

// relayLogTail returns the last lines a relay wrote, for an error message.
func relayLogTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return "(the relay said nothing)"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}

// reapRelay stops a box's door relay — its whole process group, since it leads
// its own session. A zero pid means the box was never granted a door.
func reapRelay(pid int) {
	if pid == 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// reapSidecars stops the host-side processes one box owns — its proxy engine
// and its door relay. Both pids live on the box node, so this must run BEFORE
// the node record is removed, and it works from any dabs process, including one
// that started neither.
func (r Real) reapSidecars(instance string) {
	proxy.Reap(r.boxProxy(instance))
	// A box with no door has no relay, and asking to stop one that never existed
	// is not a no-op worth making: the pid is the whole handle, so no pid is no
	// call.
	if pid := r.boxRelay(instance); pid != 0 {
		r.unrelay(pid)
	}
}

// boxRelay returns the door relay's pid as recorded on the box node named by
// instance, or 0 when the box has no door.
func (r Real) boxRelay(instance string) int {
	nodes, err := r.listNodes()
	if err != nil {
		return 0
	}
	for _, n := range nodes {
		if n.Kind == KindBox && n.Instance == instance {
			return n.RelayPID
		}
	}
	return 0
}
