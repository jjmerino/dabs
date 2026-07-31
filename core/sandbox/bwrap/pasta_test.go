//go:build linux

package bwrap

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jjmerino/dabs/core/sandbox"
)

// fakeTools puts a stub of every named binary on PATH and returns the driver
// root the test's instances live under.
func fakeTools(t *testing.T, names ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	// pasta serves an unprivileged caller only; the tests describe one whether
	// or not the suite happens to run as root.
	saved := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = saved })
	return t.TempDir()
}

// upInstance builds an instance with the given egress and returns its name.
func upInstance(t *testing.T, d Driver, egress string) string {
	t.Helper()
	img := filepath.Join(d.root, "images", "demo")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(img, "image.json"), []byte(`{"env":[],"workdir":"/work"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	instance, err := d.Up(sandbox.Spec{Name: "demo", Workdir: "/work", Egress: egress})
	if err != nil {
		t.Fatalf("Up(%q): %v", egress, err)
	}
	return instance
}

// A box with open egress must get a namespace of its own, so the command dabs
// runs is pasta's — carrying bwrap, not replacing it.
func TestEnterOpenEgressRunsBwrapUnderPasta(t *testing.T) {
	root := fakeTools(t, "bwrap", "pasta")
	d := Driver{root: root}
	instance := upInstance(t, d, sandbox.EgressOpen)

	c, err := d.enter(instance, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(c.Path); got != "pasta" {
		t.Fatalf("entered command is %q, want pasta", got)
	}
	line := strings.Join(c.Args, " ")
	for _, want := range []string{
		"--config-net",
		"--dns-forward " + dnsForwardAddr,
		"--tcp-ports none",
		"--udp-ports none",
		"--tcp-ns none",
		"--udp-ns none",
		"--map-guest-addr none",
		"--no-map-gw",
		"-- bwrap",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("pasta invocation missing %q:\n%s", want, line)
		}
	}
	// pasta owns the namespace, so bwrap must not make a second one.
	if strings.Contains(line, "--unshare-net") {
		t.Errorf("open egress must not unshare the net a second time:\n%s", line)
	}
}

// dabs is killed far more often than it exits cleanly, and pasta has no bond of
// its own to the process that started it: without a parent-death signal a killed
// dabs leaves pasta and its bwrap holding the instance's overlay while the flock
// is free for the next enter.
func TestEnterOpenEgressBindsPastaLifetimeToDabs(t *testing.T) {
	root := fakeTools(t, "bwrap", "pasta")
	d := Driver{root: root}
	instance := upInstance(t, d, sandbox.EgressOpen)

	c, err := d.enter(instance, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SysProcAttr == nil {
		t.Fatal("pasta is started with no parent-death bond")
	}
	if got := c.SysProcAttr.Pdeathsig; got != syscall.SIGKILL {
		t.Fatalf("parent-death signal is %v, want SIGKILL", got)
	}
	// A death bond the process could ignore or trap is not one.
	if c.SysProcAttr.Setpgid {
		t.Error("pasta must stay in the caller's process group, so a terminal signal reaches it too")
	}
}

// The resolver the box is pointed at must be reachable from the box's OWN
// namespace: a loopback address there is the box's loopback, not the host's,
// and answers nothing.
func TestDNSForwardAddrIsNotLoopback(t *testing.T) {
	ip := net.ParseIP(dnsForwardAddr)
	if ip == nil {
		t.Fatalf("dnsForwardAddr %q is not an address", dnsForwardAddr)
	}
	if ip.IsLoopback() {
		t.Fatalf("dnsForwardAddr %q is loopback, which in the box is the box's own", dnsForwardAddr)
	}
	if !ip.IsLinkLocalUnicast() {
		t.Errorf("dnsForwardAddr %q is not link-local, so it can collide with a real resolver", dnsForwardAddr)
	}
}

// The box resolves at pasta, never at whatever loopback stub the host's own
// resolv.conf names.
func TestEnterOpenEgressBindsPastaResolver(t *testing.T) {
	root := fakeTools(t, "bwrap", "pasta")
	d := Driver{root: root}
	instance := upInstance(t, d, sandbox.EgressOpen)

	c, err := d.enter(instance, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(c.Args, " ")
	if !strings.Contains(line, d.resolvConfPath(instance)+" /etc/resolv.conf") {
		t.Fatalf("box's /etc/resolv.conf is not the instance's:\n%s", line)
	}
	if strings.Contains(line, "/etc/resolv.conf /etc/resolv.conf") {
		t.Fatalf("host resolv.conf leaked into the box:\n%s", line)
	}
	raw, err := os.ReadFile(d.resolvConfPath(instance))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "nameserver "+dnsForwardAddr) {
		t.Fatalf("resolv.conf does not name pasta's resolver:\n%s", raw)
	}
}

// none and proxy keep the bare unshared namespace they always had: no pasta,
// no resolver.
func TestEnterRestrictedEgressUnchanged(t *testing.T) {
	for _, mode := range []string{sandbox.EgressNone, sandbox.EgressProxy} {
		root := fakeTools(t, "bwrap", "pasta")
		d := Driver{root: root}
		instance := upInstance(t, d, mode)
		c, err := d.enter(instance, []string{"true"})
		if err != nil {
			t.Fatal(err)
		}
		if got := filepath.Base(c.Path); got != "bwrap" {
			t.Fatalf("%s: entered command is %q, want bwrap", mode, got)
		}
		line := strings.Join(c.Args, " ")
		if !strings.Contains(line, "--unshare-net") {
			t.Errorf("%s: box is not cut off the network:\n%s", mode, line)
		}
		if strings.Contains(line, "/etc/resolv.conf") {
			t.Errorf("%s: a restricted box has no resolver to reach:\n%s", mode, line)
		}
	}
}

// A host without pasta cannot give a box its own connected namespace, and must
// say so at boot rather than quietly handing it the host's network.
func TestUpRefusesWithoutPasta(t *testing.T) {
	root := fakeTools(t, "bwrap")
	d := Driver{root: root}
	img := filepath.Join(root, "images", "demo")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(img, "image.json"), []byte(`{"env":[],"workdir":"/work"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := d.Up(sandbox.Spec{Name: "demo", Workdir: "/work", Egress: sandbox.EgressOpen})
	if err == nil {
		t.Fatal("Up succeeded without pasta")
	}
	for _, want := range []string{"pasta", "passt", "apt install passt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
	// A restricted box needs nothing pasta provides, so it still boots.
	if _, err := d.Up(sandbox.Spec{Name: "demo", Workdir: "/work", Egress: sandbox.EgressNone}); err != nil {
		t.Fatalf("egress none must not need pasta: %v", err)
	}
}

// pasta cannot map a namespace it built for a root caller, so a root caller is
// told so at boot instead of watching every command fail inside.
func TestUpRefusesForRootCaller(t *testing.T) {
	root := fakeTools(t, "bwrap", "pasta")
	geteuid = func() int { return 0 }
	d := Driver{root: root}
	img := filepath.Join(root, "images", "demo")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(img, "image.json"), []byte(`{"env":[],"workdir":"/work"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := d.Up(sandbox.Spec{Name: "demo", Workdir: "/work", Egress: sandbox.EgressOpen})
	if err == nil {
		t.Fatal("Up succeeded as root")
	}
	if !strings.Contains(err.Error(), "unprivileged user") {
		t.Errorf("refusal does not say what to do about it: %v", err)
	}
}

// A meta.json written before egress was recorded carries no mode at all, and an
// instance that named no egress asked for the default. Both mean open, and both
// must get the namespace open promises rather than the host's network.
func TestEnterUnsetEgressIsOpen(t *testing.T) {
	root := fakeTools(t, "bwrap", "pasta")
	d := Driver{root: root}
	instance := upInstance(t, d, "")

	c, err := d.enter(instance, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(c.Path); got != "pasta" {
		t.Fatalf("an instance with no egress recorded entered via %q, want pasta", got)
	}
	if strings.Contains(strings.Join(c.Args, " "), "--unshare-net") {
		t.Error("an instance with no egress recorded was cut off the network")
	}
}
