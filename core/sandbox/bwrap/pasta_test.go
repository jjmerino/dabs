//go:build linux

package bwrap

import (
	"os"
	"path/filepath"
	"strings"
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
