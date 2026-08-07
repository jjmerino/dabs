package door_test

// Contract tests for the egress crossing: a proxy box's way out rides the
// door, one crossing per proxied connection, coupled by the relay to the host
// proxy's socket — dialed for that crossing alone.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jjmerino/dabs/core/door"
)

// egressRelay stands a relay whose EGRESS crossings couple to a fake proxy
// that prefixes "got " to one line, counting the connections it accepted.
func egressRelay(t *testing.T) (doorPath string, accepted *int32) {
	t.Helper()
	dir := sockDir(t)
	doorPath = filepath.Join(dir, "door.sock")
	engine := filepath.Join(dir, "engine.sock")
	ln, err := net.Listen("unix", engine)
	if err != nil {
		t.Fatalf("engine listener: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	var hits int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&hits, 1)
			go func(c net.Conn) {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprintf(c, "got %s", line)
			}(conn)
		}
	}()
	r := door.NewRelay("", io.Discard)
	r.Egress = engine
	if err := r.Open(doorPath); err != nil {
		t.Fatalf("open: %v", err)
	}
	go func() { _ = r.Serve() }()
	t.Cleanup(func() { _ = r.Close() })
	return doorPath, &hits
}

// CONTRACT: an egress crossing carries bytes to the host proxy and the answer
// back — dialed, declared EGRESS, answered OK, then raw both ways.
func TestEgressCrossingRoundTrips(t *testing.T) {
	doorPath, _ := egressRelay(t)
	conn, err := door.DialEgress(doorPath)
	if err != nil {
		t.Fatalf("DialEgress: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintln(conn, "hello")
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply != "got hello\n" {
		t.Fatalf("reply = %q, want %q", reply, "got hello\n")
	}
}

// CONTRACT: each proxied connection is its own crossing and its own dial on
// the proxy — never a stream on a shared connection. What identifies a caller
// (a credential reads off a connection) attaches per crossing, so collapsing
// them would collapse every caller into one.
func TestEachEgressCrossingIsItsOwnConnection(t *testing.T) {
	doorPath, accepted := egressRelay(t)
	const n = 3
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := door.DialEgress(doorPath)
			if err != nil {
				t.Errorf("DialEgress %d: %v", i, err)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprintf(conn, "c%d\n", i)
			reply, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil || reply != fmt.Sprintf("got c%d\n", i) {
				t.Errorf("crossing %d got %q, %v — bytes crossed between callers", i, reply, err)
			}
		}(i)
	}
	wg.Wait()
	if got := atomic.LoadInt32(accepted); got != n {
		t.Fatalf("the proxy saw %d connections for %d crossings — a shared connection collapses per-caller identity", got, n)
	}
}

// CONTRACT: a door with nothing behind EGRESS refuses it as a decision —
// an ERR the box side gives up on, saying what this box does not have.
func TestDoorWithoutEgressRefusesByName(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	r := door.NewRelay(filepath.Join(dir, "registry"), io.Discard)
	if err := r.Open(doorPath); err != nil {
		t.Fatalf("open: %v", err)
	}
	go func() { _ = r.Serve() }()
	t.Cleanup(func() { _ = r.Close() })

	_, err := door.DialEgress(doorPath)
	var refused door.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("EGRESS on a door with no proxy = %v, want the door's own ERR", err)
	}
	if !strings.Contains(refused.Reason, "no proxy egress") {
		t.Errorf("the refusal %q does not name what is missing", refused.Reason)
	}
}

// CONTRACT: EGRESS takes no arguments — where the bytes go was decided when
// the box was booted, and a crossing that tries to name a destination is
// refused rather than partially obeyed.
func TestEgressWithArgumentsIsRefused(t *testing.T) {
	doorPath, _ := egressRelay(t)
	conn, br := openCrossing(t, doorPath, door.Banner+" "+door.VerbEgress+" /tmp/other.sock")
	defer conn.Close()
	err := door.ReadReply(br)
	var refused door.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("EGRESS with an argument = %v, want ERR", err)
	}
}
