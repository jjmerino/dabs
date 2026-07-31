package forwarder_test

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// CONTRACT: publishing writes a descriptor beside a socket, and the socket
// carries bytes to the box-local port both ways — the whole registration.
func TestPublishBridgesSocketToLocalPort(t *testing.T) {
	dir := shortTempDir(t)
	// The "box-local" service: it answers with a prefix of its own, so the reply
	// proves the bytes reached THIS listener and came back.
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer svc.Close()
	go func() {
		for {
			c, err := svc.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("served:"), b...))
			}()
		}
	}()
	port := svc.Addr().(*net.TCPAddr).Port
	fail := make(chan error, 1)
	go func() { fail <- forwarder.Publish(dir, "web", forwarder.TypeWebUI, port) }()

	sock := filepath.Join(dir, forwarder.SocketName("web"))
	conn := dialWhenReady(t, sock, fail)
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.(*net.UnixConn).CloseWrite()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "served:hello" {
		t.Errorf("through the socket = %q, want %q", got, "served:hello")
	}

	b, err := os.ReadFile(filepath.Join(dir, forwarder.DescriptorName("web")))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	var d forwarder.Descriptor
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("descriptor json: %v", err)
	}
	if d.Type != forwarder.TypeWebUI || d.Port != port {
		t.Errorf("descriptor = %+v, want type %s port %d", d, forwarder.TypeWebUI, port)
	}
}

// CONTRACT: a socket file left by a dead publisher does not block a new one —
// a re-upped box publishes the same name into the same mounted directory.
func TestPublishReplacesAStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, forwarder.SocketName("api"))
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer svc.Close()
	fail := make(chan error, 1)
	go func() { fail <- forwarder.Publish(dir, "api", forwarder.TypeGeneral, svc.Addr().(*net.TCPAddr).Port) }()
	dialWhenReady(t, sock, fail).Close()
}

// dialWhenReady waits for the publisher's listener to exist and connects,
// failing early with the publisher's own error if it gave up.
func dialWhenReady(t *testing.T, sock string, fail <-chan error) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			return conn
		}
		select {
		case err := <-fail:
			t.Fatalf("Publish: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never accepted", sock)
	return nil
}

// shortTempDir is a temp directory with a SHORT path: a unix socket path has a
// hard length limit (about 100 bytes), and a directory named after the test
// blows through it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "d")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// CONTRACT: only a name that is safe as a filename AND as a cell in the host's
// listing is publishable. The box is the untrusted side: a name carrying a
// newline or an escape sequence would forge rows on the host's terminal.
func TestPublishRefusesNamesOutsideTheAllowlist(t *testing.T) {
	dir := shortTempDir(t)
	for _, name := range []string{
		"",
		".",
		"..",
		"a/b",
		"a\\b",
		"web\nweb2  general  127.0.0.1:42000  up",
		"web\x1b[31m",
		"web\x00",
		"WEB",
		"-web",
		"web service",
		strings.Repeat("w", forwarder.MaxServiceNameLen+1),
	} {
		if err := forwarder.CheckServiceName(name); err == nil {
			t.Errorf("CheckServiceName(%q) = nil, wanted a refusal", name)
		}
		// Publish must apply the same rule. It serves for ever once it accepts,
		// so a refusal is what has to come back — and quickly.
		refused := make(chan error, 1)
		go func(name string) { refused <- forwarder.Publish(dir, name, forwarder.TypeWebUI, 8080) }(name)
		select {
		case err := <-refused:
			if err == nil {
				t.Errorf("Publish(%q) = nil, wanted a refusal", name)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("Publish(%q) did not refuse — it is serving a name it should not have taken", name)
		}
	}
	for _, name := range []string{"web", "web-ui", "web_ui", "web.ui", "5173", strings.Repeat("w", forwarder.MaxServiceNameLen)} {
		if err := forwarder.CheckServiceName(name); err != nil {
			t.Errorf("CheckServiceName(%q) = %v, wanted it allowed", name, err)
		}
	}
}

func TestPublishRefusesUnknownTypes(t *testing.T) {
	if err := forwarder.Publish(shortTempDir(t), "web", "dashboard", 8080); err == nil {
		t.Error("Publish with an unknown type = nil, wanted a refusal")
	}
}
