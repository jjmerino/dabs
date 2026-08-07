package forwarder

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"testing"

	"github.com/jjmerino/dabs/core/door"
)

// fakeDoor answers one egress crossing the way a box's relay does — header in,
// OK out — and hands the connection to serve, so the test measures the
// forwarder's own side of the protocol against the door's real wire shape.
func fakeDoor(t *testing.T, doorPath string, serve func(net.Conn)) {
	t.Helper()
	ln, err := net.Listen("unix", doorPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		h, err := door.ReadHeader(br)
		if err != nil || h.Verb != door.VerbEgress {
			_ = door.WriteReply(conn, fmt.Errorf("not an egress crossing"))
			return
		}
		if err := door.WriteReply(conn, nil); err != nil {
			return
		}
		serve(conn)
	}()
}

// CONTRACT: bytes written to the TCP side cross the door as ONE egress
// crossing — dialed, declared EGRESS, answered OK — and the reply comes back;
// a proxy client in a box reaches the host proxy unchanged. The client
// half-closes after sending, and the reply must still arrive whole.
func TestForwardRoundTrips(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "door.sock")
	fakeDoor(t, sock, func(conn net.Conn) { // a "got "-prefixing echo stands in for the proxy
		line, _ := bufio.NewReader(conn).ReadString('\n')
		fmt.Fprintf(conn, "got %s", line)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serve(ln, sock)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintln(conn, "hello")
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if reply != "got hello\n" {
		t.Fatalf("reply = %q, want %q", reply, "got hello\n")
	}
}

// CONTRACT: WrapCommand brackets the box command with the forwarder — the
// mounted dabs binary, the box door, the port, then the argv behind `--`.
func TestWrapCommand(t *testing.T) {
	got := WrapCommand([]string{"sleep", "infinity"})
	want := []string{ForwardPath, door.BoxPath, "18080", "--", "sleep", "infinity"}
	if len(got) != len(want) {
		t.Fatalf("wrapped = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrapped = %v, want %v", got, want)
		}
	}
}
