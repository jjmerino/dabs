package forwarder

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"testing"
)

// CONTRACT: bytes written to the TCP side come out of the unix socket and the
// reply comes back — a proxy client in a box reaches the host proxy unchanged.
// The client half-closes after sending, and the reply must still arrive whole.
func TestForwardRoundTrips(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "p.sock")
	usock, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer usock.Close()
	go func() { // a "got "-prefixing echo stands in for the proxy
		conn, err := usock.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		fmt.Fprintf(conn, "got %s", line)
	}()

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
// mounted dabs binary, the verb, the in-box socket and port, then the argv
// behind `--`.
func TestWrapCommand(t *testing.T) {
	got := WrapCommand([]string{"sleep", "infinity"})
	want := []string{ForwardPath, SockPath, "18080", "--", "sleep", "infinity"}
	if len(got) != len(want) {
		t.Fatalf("wrapped = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrapped = %v, want %v", got, want)
		}
	}
}
