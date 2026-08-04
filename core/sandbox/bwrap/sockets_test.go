//go:build linux

package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/sandbox"
)

// stageImage writes the minimal image a driver needs to bring an instance up:
// a rootfs dir and the image.json Up reads for env and workdir.
func stageImage(t *testing.T, d Driver, name string) {
	t.Helper()
	dir := d.imageDir(name)
	if err := os.MkdirAll(filepath.Join(dir, "rootfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "image.json"), imageMeta{Workdir: "/work"}); err != nil {
		t.Fatal(err)
	}
}

// findArg returns the position of tok in args, or -1.
func findArg(args []string, tok string) int {
	for i, a := range args {
		if a == tok {
			return i
		}
	}
	return -1
}

// CONTRACT: this driver has no long-lived process — every command re-enters the
// box with a fresh bwrap, reading only what Up persisted. So a socket declared at
// boot must still be bound on a LATER enter, or an `exec` after an `up` would
// find the door gone. It is bound after the recipe's own mounts (bwrap binds in
// argv order, and a mount over the socket's directory would mask it) and
// read-write (connecting to a unix socket writes its inode).
func TestSocketsSurviveEveryEnter(t *testing.T) {
	d := Driver{root: t.TempDir()}
	stageImage(t, d, "img")
	instance, err := d.Up(sandbox.Spec{
		Name: "img", Workdir: "/work", Egress: sandbox.EgressNone,
		Mounts:  []sandbox.Mount{{Host: "/host/data", Path: "/run/dabs"}},
		Sockets: []sandbox.Mount{{Host: "/host/one.sock", Path: "/run/dabs/one.sock"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, enter := range []string{"first", "second"} {
		t.Run(enter, func(t *testing.T) {
			c, err := d.enter(instance, []string{"true"})
			if err != nil {
				t.Fatal(err)
			}
			args := c.Args
			bind := findArg(args, "/host/one.sock")
			if bind < 1 || args[bind-1] != "--bind" || args[bind+1] != "/run/dabs/one.sock" {
				t.Fatalf("socket not bound read-write at its box path: %v", args)
			}
			mount := findArg(args, "/host/data")
			if mount < 0 || mount > bind {
				t.Errorf("socket bound before the recipe mount that could mask it: %v", args)
			}
		})
	}
}

// CONTRACT: a box whose recipe declares no sockets binds none — the only binds
// on its argv are the ones it always had.
func TestNoSocketsBindsNone(t *testing.T) {
	d := Driver{root: t.TempDir()}
	stageImage(t, d, "img")
	instance, err := d.Up(sandbox.Spec{Name: "img", Workdir: "/work", Egress: sandbox.EgressNone})
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.enter(instance, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if i := findArg(c.Args, "--bind"); i >= 0 {
		t.Errorf("a socketless, mountless box binds something: %v", c.Args)
	}
	if !strings.Contains(strings.Join(c.Args, " "), "--unshare-net") {
		t.Errorf("egress none did not cut the network: %v", c.Args)
	}
}
