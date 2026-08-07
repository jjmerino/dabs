package door_test

// Contract tests for carried sockets: a recipe `socket:` as the relay serves
// it. The contract under guard is DIAL-PER-CONNECTION — the host program's
// listener restarting must never finish the mount, whatever landed during the
// gap — because the mount the box holds is established against the relay's own
// listener, whose life is the relay's.

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jjmerino/dabs/core/door"
)

// echoServe answers every connection on ln by echoing one byte back.
func echoServe(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 1)
			if _, err := io.ReadFull(c, buf); err == nil {
				_, _ = c.Write(buf)
			}
		}(conn)
	}
}

// echoThrough dials the carried socket and round-trips one byte.
func echoThrough(listen string, b byte) error {
	conn, err := net.Dial("unix", listen)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte{b}); err != nil {
		return err
	}
	got := make([]byte, 1)
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if got[0] != b {
		return errors.New("echoed a different byte")
	}
	return nil
}

// carriedRelay stands a relay carrying one socket, and returns the paths.
func carriedRelay(t *testing.T, dialWait time.Duration) (listen, hostSock string) {
	t.Helper()
	dir := sockDir(t)
	listen = filepath.Join(dir, "carry0.sock")
	hostSock = filepath.Join(dir, "host.sock")
	r := door.NewRelay("", io.Discard)
	r.DialWait = dialWait
	r.Carries = []door.Carry{{Listen: listen, Dial: hostSock}}
	if err := r.Open(filepath.Join(dir, "door.sock")); err != nil {
		t.Fatalf("open: %v", err)
	}
	go func() { _ = r.Serve() }()
	t.Cleanup(func() { _ = r.Close() })
	return listen, hostSock
}

// CONTRACT: a dial that lands while the host program's listener is down fails
// ALONE. The next dial after the listener returns succeeds — one refusal must
// never finish the mount for the box's life, because a box's whole reason to
// hold the mount is to talk to a host program, and host programs restart.
func TestCarriedSocketSurvivesADialInTheListenerGap(t *testing.T) {
	listen, hostSock := carriedRelay(t, 150*time.Millisecond)

	ln, err := net.Listen("unix", hostSock)
	if err != nil {
		t.Fatalf("host listener: %v", err)
	}
	go echoServe(ln)
	if err := echoThrough(listen, 'a'); err != nil {
		t.Fatalf("first crossing: %v", err)
	}

	// The host program restarts: its listener closes (unlinking the socket),
	// and a dial lands in the gap. That crossing may fail — it is the one that
	// caught the program down — and here it must, since nothing comes back
	// inside this relay's short DialWait.
	_ = ln.Close()
	if err := echoThrough(listen, 'b'); err == nil {
		t.Fatal("a crossing dialed into the gap succeeded against nothing — the fixture is not measuring a gap")
	}

	ln2, err := net.Listen("unix", hostSock)
	if err != nil {
		t.Fatalf("host listener, restarted: %v", err)
	}
	defer ln2.Close()
	go echoServe(ln2)
	if err := echoThrough(listen, 'c'); err != nil {
		t.Fatalf("the crossing after the restart failed: %v — the dial in the gap finished the mount", err)
	}
}

// CONTRACT: a crossing that catches the host program mid-restart is HELD, up
// to DialWait, and completes when the listener returns — the caller never
// learns the restart happened.
func TestCarriedCrossingHeldAcrossARestart(t *testing.T) {
	listen, hostSock := carriedRelay(t, 5*time.Second)

	ln, err := net.Listen("unix", hostSock)
	if err != nil {
		t.Fatalf("host listener: %v", err)
	}
	go echoServe(ln)
	if err := echoThrough(listen, 'a'); err != nil {
		t.Fatalf("first crossing: %v", err)
	}
	_ = ln.Close()

	// The listener returns while the crossing below is still inside its
	// DialWait window.
	go func() {
		time.Sleep(300 * time.Millisecond)
		ln2, err := net.Listen("unix", hostSock)
		if err != nil {
			return
		}
		echoServe(ln2)
	}()
	if err := echoThrough(listen, 'b'); err != nil {
		t.Fatalf("a crossing inside the DialWait window failed: %v", err)
	}
}

// CONTRACT: the relay's own listeners come down with it, and the socket files
// they answered are what a next relay may replace — a carried socket is the
// relay's, not debris the box node keeps.
func TestClosingTheRelayClosesItsCarriedListeners(t *testing.T) {
	dir := sockDir(t)
	listen := filepath.Join(dir, "carry0.sock")
	hostSock := filepath.Join(dir, "host.sock")
	ln, err := net.Listen("unix", hostSock)
	if err != nil {
		t.Fatalf("host listener: %v", err)
	}
	defer ln.Close()
	r := door.NewRelay("", io.Discard)
	r.Carries = []door.Carry{{Listen: listen, Dial: hostSock}}
	if err := r.Open(filepath.Join(dir, "door.sock")); err != nil {
		t.Fatalf("open: %v", err)
	}
	go func() { _ = r.Serve() }()
	if _, err := os.Stat(listen); err != nil {
		t.Fatalf("the carried listener is not standing: %v", err)
	}
	_ = r.Close()
	conn, err := net.DialTimeout("unix", listen, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("a closed relay's carried socket still answers")
	}
}

// CONTRACT: a door with no registry belongs to a box that may not publish, and
// a PUBLISH crossing on it is refused BY NAME — an ERR that says what the
// recipe must grant, not a missing file and not a door that hangs up.
func TestDoorWithoutRegistryRefusesPublishByName(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	r := door.NewRelay("", io.Discard)
	r.Carries = []door.Carry{{Listen: filepath.Join(dir, "carry0.sock"), Dial: filepath.Join(dir, "host.sock")}}
	if err := r.Open(doorPath); err != nil {
		t.Fatalf("open: %v", err)
	}
	go func() { _ = r.Serve() }()
	t.Cleanup(func() { _ = r.Close() })

	conn, br := openCrossing(t, doorPath, publishLine("web", 3000))
	defer conn.Close()
	err := door.ReadReply(br)
	var refused door.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("publish on an ungranted door = %v, want the door's own ERR", err)
	}
	if want := "publish: true"; !strings.Contains(refused.Reason, want) {
		t.Errorf("the refusal %q does not name the grant (%q)", refused.Reason, want)
	}
}
