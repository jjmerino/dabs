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

func TestPublishRefusesUnusableNamesAndTypes(t *testing.T) {
	dir := shortTempDir(t)
	for _, tc := range []struct{ name, typ string }{
		{"", forwarder.TypeWebUI},
		{"..", forwarder.TypeWebUI},
		{"a/b", forwarder.TypeWebUI},
		{"web", "dashboard"},
	} {
		if err := forwarder.Publish(dir, tc.name, tc.typ, 8080); err == nil {
			t.Errorf("Publish(%q, %q) = nil, want a refusal", tc.name, tc.typ)
		}
	}
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

// CONTRACT: a service exists exactly when its socket does. A descriptor that
// cannot be written takes the socket file with it, so no host ever finds a
// socket describing nothing.
func TestPublishRemovesTheSocketWhenTheDescriptorCannotBeWritten(t *testing.T) {
	dir := shortTempDir(t)
	// A directory in the descriptor's place: the rename onto it fails, which is
	// the failure every other cause reduces to.
	if err := os.MkdirAll(filepath.Join(dir, forwarder.DescriptorName("web")), 0o755); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer svc.Close()
	if err := forwarder.Publish(dir, "web", forwarder.TypeWebUI, svc.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("Publish = nil, want the descriptor failure")
	}
	if _, err := os.Stat(filepath.Join(dir, forwarder.SocketName("web"))); err == nil {
		t.Error("the socket file was left behind by a publish that failed")
	}
}

// CONTRACT: the descriptor appears whole. It is written by rename, so a host
// scanning the directory never parses a truncated one — and the scratch file it
// is renamed from is not mistaken for a descriptor.
func TestDescriptorIsWrittenByRename(t *testing.T) {
	dir := shortTempDir(t)
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer svc.Close()
	fail := make(chan error, 1)
	go func() { fail <- forwarder.Publish(dir, "web", forwarder.TypeWebUI, svc.Addr().(*net.TCPAddr).Port) }()
	dialWhenReady(t, filepath.Join(dir, forwarder.SocketName("web")), fail).Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && e.Name() != forwarder.DescriptorName("web") {
			t.Errorf("scratch file %q left in the services dir", e.Name())
		}
	}
	var d forwarder.Descriptor
	b, err := os.ReadFile(filepath.Join(dir, forwarder.DescriptorName("web")))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("descriptor is not whole: %v", err)
	}
}
