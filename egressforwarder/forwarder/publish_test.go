package forwarder_test

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	go func() { fail <- forwarder.Publish(dir, "web", forwarder.TypeWebUI, port, false) }()

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
	go func() {
		fail <- forwarder.Publish(dir, "api", forwarder.TypeGeneral, svc.Addr().(*net.TCPAddr).Port, false)
	}()
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
		go func(name string) { refused <- forwarder.Publish(dir, name, forwarder.TypeWebUI, 8080, false) }(name)
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
	if err := forwarder.Publish(shortTempDir(t), "web", "dashboard", 8080, false); err == nil {
		t.Error("Publish with an unknown type = nil, wanted a refusal")
	}
}

// CONTRACT: the outward door is OFF unless it was asked for. A box that shares
// the host's network namespace would otherwise have this listener standing on
// the host's own interfaces — an unauthenticated way into the box's service
// from anything that can reach the host.
func TestPublishOpensNoNetworkListenerUnlessAsked(t *testing.T) {
	dir := shortTempDir(t)
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer svc.Close()
	before := listeningPorts(t)
	fail := make(chan error, 1)
	go func() {
		fail <- forwarder.Publish(dir, "web", forwarder.TypeWebUI, svc.Addr().(*net.TCPAddr).Port, false)
	}()
	dialWhenReady(t, filepath.Join(dir, forwarder.SocketName("web")), fail).Close()

	var d forwarder.Descriptor
	b, err := os.ReadFile(filepath.Join(dir, forwarder.DescriptorName("web")))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	if d.Bridge != 0 {
		t.Errorf("descriptor carries bridge %d; nothing asked for one", d.Bridge)
	}
	if strings.Contains(string(b), "bridge") {
		t.Errorf("descriptor names a bridge it does not have: %s", b)
	}
	for addr := range listeningPorts(t) {
		if !before[addr] {
			t.Errorf("a listener appeared at %s; publish opened a door nobody asked for", addr)
		}
	}
}

// CONTRACT: asked for, the door is open and the descriptor says where.
func TestPublishOpensTheNetworkListenerWhenAsked(t *testing.T) {
	dir := shortTempDir(t)
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
			go func() { defer c.Close(); _, _ = c.Write([]byte("served")) }()
		}
	}()
	fail := make(chan error, 1)
	go func() {
		fail <- forwarder.Publish(dir, "web", forwarder.TypeWebUI, svc.Addr().(*net.TCPAddr).Port, true)
	}()
	dialWhenReady(t, filepath.Join(dir, forwarder.SocketName("web")), fail).Close()
	b, err := os.ReadFile(filepath.Join(dir, forwarder.DescriptorName("web")))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	var d forwarder.Descriptor
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	if d.Bridge == 0 {
		t.Fatalf("descriptor carries no bridge port: %s", b)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(d.Bridge)), 2*time.Second)
	if err != nil {
		t.Fatalf("the bridge port does not answer: %v", err)
	}
	defer conn.Close()
	got := make([]byte, len("served"))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "served" {
		t.Errorf("through the bridge = %q, %v; want the service's answer", got, err)
	}
}

// CONTRACT: dabs says in the environment whether the box needs the outward
// door, and the flag's default follows it — the box cannot know by itself.
func TestBridgeWantedReadsTheEnvironment(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "true": true, "": false, "0": false, "no": false} {
		if got := forwarder.BridgeWanted(value); got != want {
			t.Errorf("BridgeWanted(%q) = %v, want %v", value, got, want)
		}
	}
}

// listeningPorts is the set of TCP listening sockets on this machine, as
// "address:port" strings. It reads the kernel's own table on Linux — the
// platform where a bwrap box shares this network namespace, so the platform
// this test exists for — and the vendor tool elsewhere.
func listeningPorts(t *testing.T) map[string]bool {
	t.Helper()
	if runtime.GOOS == "linux" {
		return procListeners(t)
	}
	out, err := exec.Command("netstat", "-an", "-p", "tcp").Output()
	if err != nil {
		t.Skipf("cannot list listening sockets on %s (%v); NOT guarding that publish opens no network listener", runtime.GOOS, err)
	}
	ports := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ports[fields[3]] = true
	}
	return ports
}

// procListeners reads Linux's TCP tables and returns the sockets in LISTEN
// state (0x0A), keyed by the hex address:port the kernel prints.
func procListeners(t *testing.T) map[string]bool {
	t.Helper()
	const listen = "0A"
	ports := map[string]bool{}
	read := 0
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // tcp6 is absent on a kernel without IPv6; tcp is not
		}
		read++
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != listen {
				continue
			}
			ports[fields[1]] = true
		}
	}
	if read == 0 {
		t.Fatal("/proc/net/tcp is unreadable; this test cannot guard that publish opens no network listener")
	}
	return ports
}
