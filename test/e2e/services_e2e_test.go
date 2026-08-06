//go:build e2e

// Services end to end: a program bound to box-local loopback, published by the
// in-box forwarder, listed by `dabs services`, and reached from the host
// through `dabs services serve`. Nothing here talks to the internet — the
// service is a fixture the suite builds and the box runs.
package e2e

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// svcBinDir is the host directory the `svcbox` recipe mounts at /opt/bin — the
// forwarder and the fixture responder, built by the suite for the box to run.
func svcBinDir() string { return filepath.Join(home, ".dabs", "e2e-svcbin") }

// buildBoxBinaries builds the forwarder and the fixture responder into the
// directory the box mounts. The box carries no Go toolchain; the suite's own
// box does, and a mounted binary is how the forwarder reaches a box anyway.
func buildBoxBinaries(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(svcBinDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []struct{ out, path string }{
		{"forward", "./egressforwarder/cmd/forward"},
		{"responder", "./test/e2e/responder"},
	} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(svcBinDir(), pkg.out), pkg.path)
		cmd.Dir = filepath.Dir(filepath.Dir(baseDir)) // the repo root, two above test/e2e
		// The box that runs these carries no dynamic loader, and Go links net
		// against libc when cgo is available — a dynamically linked binary lands
		// in the box as `not found`.
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg.path, err, out)
		}
	}
}

// publishInBox starts the responder and the publisher inside the box and keeps
// them alive for the test: on a driver that enters the box with a fresh process
// per command, the box-side processes live exactly as long as this exec does,
// so the exec is held open and killed at test end.
func publishInBox(t *testing.T, node, name, typ string, port int) {
	t.Helper()
	line := "/opt/bin/responder --port " + strconv.Itoa(port) + " --body served-from-the-box & " +
		"exec /opt/bin/forward publish " + name + " --type " + typ + " --port " + strconv.Itoa(port)
	cmd := exec.Command("dabs", "exec", node, line)
	// The box-side processes report their trouble on these streams and nowhere
	// else; a publish that never happens has to be able to say why.
	var say lockedBuffer
	cmd.Stdout, cmd.Stderr = &say, &say
	if err := cmd.Start(); err != nil {
		t.Fatalf("publish in box: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if out := say.String(); out != "" && t.Failed() {
			t.Logf("in-box publish said:\n%s", out)
		}
	})
}

// lockedBuffer collects a running command's output while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls until cond holds, failing the test with why if it never does.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", why)
}

// CONTRACT: a box publishes a name, `dabs services` lists it as up, and
// `dabs services serve` makes it answer on a host loopback port — the whole
// path, with no host port ever chosen by the program in the box.
func TestPublishedServiceIsListedAndReachableFromTheHost(t *testing.T) {
	clean(t)
	installRecipes(t)
	buildBoxBinaries(t)
	const node = "e2e-svc"
	defer run("dabs rm " + node + " --yes")
	if out, code := run("dabs recipe svcbox --no-command --name " + node); code != 0 {
		t.Fatalf("boot failed (%d): %s", code, out)
	}
	publishInBox(t, node, "e2eweb", "webui", 5173)

	sock := filepath.Join(nodesDir(), node, "tmp", "services", "e2eweb.sock")
	waitFor(t, "the box to publish "+sock, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	})

	// Listed, with the box that published it, and answering.
	out, code := run("dabs services")
	wantExit(t, 0, code)
	wantContains(t, out, "e2eweb")
	wantContains(t, out, "webui")
	wantContains(t, out, node)
	wantContains(t, out, "up")

	// Served: the host port round-trips to the responder in the box.
	serve := exec.Command("dabs", "services", "serve")
	if err := serve.Start(); err != nil {
		t.Fatalf("services serve: %v", err)
	}
	t.Cleanup(func() {
		_ = serve.Process.Kill()
		_, _ = serve.Process.Wait()
	})
	addr := ""
	waitFor(t, "a host address to be assigned to e2eweb", func() bool {
		out, _ := run("dabs services")
		addr = hostAddr(out, "e2eweb")
		return addr != ""
	})
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	var body string
	waitFor(t, "http://"+addr+" to answer from the box", func() bool {
		resp, err := client.Get("http://" + addr)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return body != ""
	})
	if body != "served-from-the-box" {
		t.Errorf("through the host port = %q, want the responder's body", body)
	}

	// The index names the service and links it, because it is a webui.
	resp, err := client.Get("http://127.0.0.1:28080")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(page), `<a href="http://`+addr+`">e2eweb</a>`) {
		t.Errorf("index does not link the webui at %s:\n%s", addr, page)
	}
}

// CONTRACT: a box whose recipe does not grant publishing cannot publish, and is
// told so BY NAME: the refusal says the box was not granted service publishing
// and what the recipe must set, exits nonzero, and leaves nothing half-created.
func TestAnUngrantedBoxIsRefusedThePublish(t *testing.T) {
	clean(t)
	installRecipes(t)
	buildBoxBinaries(t)
	const node = "e2e-mute"
	defer run("dabs rm " + node + " --yes")
	if out, code := run("dabs recipe mutebox --no-command --name " + node); code != 0 {
		t.Fatalf("boot failed (%d): %s", code, out)
	}
	out, code := run("dabs exec " + node + " /opt/bin/forward publish muted --type general --port 5173")
	if code == 0 {
		t.Fatalf("an ungranted box published: %s", out)
	}
	for _, want := range []string{"not granted", "publish: true"} {
		wantContains(t, out, want)
	}
	// Nothing listed, and no registry standing for a box that may not publish.
	listing, _ := run("dabs services")
	if strings.Contains(listing, "muted") {
		t.Errorf("`dabs services` lists a service the box was refused:\n%s", listing)
	}
	if _, err := os.Stat(filepath.Join(nodesDir(), node, "tmp", "services")); err == nil {
		t.Errorf("an ungranted box was given a registry directory")
	}
}

// hostAddr picks the 127.0.0.1:<port> cell out of the `dabs services` row for
// name, or "" when the name has no address yet.
func hostAddr(listing, name string) string {
	for _, line := range strings.Split(listing, "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "127.0.0.1:") {
				return field
			}
		}
	}
	return ""
}
