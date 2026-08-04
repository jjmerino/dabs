//go:build e2e

// Host sockets end to end: a recipe's `sockets:` entry, a listener on the box's
// own host, and a program inside a nested box connecting to it. The listener is
// this test process — the suite runs on the host of every box it boots — so
// nothing here reaches the internet, and a hit counted here is a byte that
// crossed the box boundary.
package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// socketRecipe writes a dabs.yaml at dir declaring ONE socket, and returns
// nothing: the recipe is named and booted from dir like every other fixture.
func socketRecipe(t *testing.T, dir, name, hostSock, boxPath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("recipes:\n  %s:\n    image: dabs-e2e\n    command: [sh]\n"+
		"    sockets:\n      - socket: %s\n        path: %s\n", name, hostSock, boxPath)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serveOnUnixSocket listens on path and answers every request with body,
// counting the requests it served. The listener is torn down with the test.
func serveOnUnixSocket(t *testing.T, path, body string) *int32 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	// The box runs as root and the listener does not, so the socket must be
	// connectable by another user — this is a fixture on a throwaway machine.
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	var hits int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		os.Remove(path)
	})
	return &hits
}

// CONTRACT: a socket a recipe declares is a live door. The box gets it at the
// path the recipe named, a program inside connects to it, and the bytes reach
// the listener on the host — under the box's default egress, which is not what
// carries them (a unix socket is filesystem, not network).
func TestSocketFromRecipeReachesTheHostListener(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-sockets")
	sock := filepath.Join(dir, "run", "probe.sock")
	hits := serveOnUnixSocket(t, sock, "reached-the-host")
	socketRecipe(t, dir, "sockprobe", sock, "/run/dabs/probe.sock")

	const node = "e2e-sock"
	defer run("dabs rm " + node + " --yes")
	out, code := runIn(dir, "dabs recipe sockprobe --no-command --name "+node)
	if code != 0 {
		t.Fatalf("boot failed (%d): %s", code, out)
	}

	// The box sees a socket, not a directory and not a regular file.
	ls, code := run("dabs exec " + node + " 'ls -l /run/dabs/probe.sock'")
	if code != 0 {
		t.Fatalf("stat in box failed (%d): %s", code, ls)
	}
	if !strings.HasPrefix(strings.TrimSpace(ls), "s") {
		t.Errorf("in-box /run/dabs/probe.sock is not a socket: %s", ls)
	}

	got, code := run("dabs exec " + node + " 'curl -s --unix-socket /run/dabs/probe.sock http://localhost/'")
	if code != 0 {
		t.Fatalf("connect from box failed (%d): %s", code, got)
	}
	wantContains(t, got, "reached-the-host")
	if n := atomic.LoadInt32(hits); n < 1 {
		t.Errorf("the host listener served %d requests — the box's bytes never arrived", n)
	}
}

// CONTRACT: a socket dabs cannot honour refuses the BOOT, by name. A host path
// with no socket on it, and a box path landing where dabs binds its own, both
// leave no box behind — the box is never created and then found wanting.
func TestSocketRefusalsLeaveNoBox(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-sockets-refuse")
	live := filepath.Join(dir, "run", "live.sock")
	serveOnUnixSocket(t, live, "unused")

	for _, tc := range []struct{ name, hostSock, boxPath, want string }{
		{"missing", filepath.Join(dir, "run", "absent.sock"), "/run/dabs/probe.sock", "does not exist"},
		{"reserved", live, "/run/dabs/services", "collides"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := "e2e-sock-" + tc.name
			defer run("dabs rm " + node + " --yes")
			socketRecipe(t, dir, "sockbad", tc.hostSock, tc.boxPath)
			out, code := runIn(dir, "dabs recipe sockbad --no-command --name "+node)
			if code == 0 {
				t.Fatalf("a %s socket booted a box: %s", tc.name, out)
			}
			wantContains(t, out, tc.want)
			listing, _ := run("dabs ls")
			if strings.Contains(listing, node) {
				t.Errorf("a refused boot left a box behind:\n%s", listing)
			}
		})
	}
}
