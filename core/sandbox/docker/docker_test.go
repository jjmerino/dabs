package docker

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/sandbox"
)

// execFlags must allocate a pseudo-TTY only when the caller's stdin is a real
// terminal: -t on a non-TTY stdin makes `docker exec` fail, which would break
// every non-interactive run (pipes, scripts, agents, CI).
func TestExecFlags(t *testing.T) {
	if got, want := execFlags(true), []string{"exec", "-i", "-t"}; !reflect.DeepEqual(got, want) {
		t.Errorf("execFlags(true) = %v, want %v", got, want)
	}
	if got, want := execFlags(false), []string{"exec", "-i"}; !reflect.DeepEqual(got, want) {
		t.Errorf("execFlags(false) = %v, want %v", got, want)
	}
}

// fakeDocker swaps the command seam for one that never touches a docker daemon:
// `inspect` (the existence probe) succeeds, and every other verb fails non-zero
// after printing marker to stdout — enough to exercise the error-surfacing path.
func fakeDocker(t *testing.T, marker string) {
	t.Helper()
	orig := command
	command = func(_ string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "inspect" {
			return exec.Command("sh", "-c", "exit 0")
		}
		return exec.Command("sh", "-c", "echo "+marker+"; exit 7")
	}
	t.Cleanup(func() { command = orig })
}

// The four drivers must surface a box subprocess the same way: Run and Exec
// return the box command's own non-zero exit BARE (a directly type-asserted
// *exec.ExitError, so main mirrors the code and prints no dabs line), while
// Up and RemoveImage — dabs's own machinery — wrap with the vendor output.
// This pins the docker driver to that shared policy: before consolidation its
// Run/Exec wrapped unconditionally, so the bare-ExitError assertions were red.
func TestErrorPolicy(t *testing.T) {
	t.Run("Run returns bare ExitError", func(t *testing.T) {
		fakeDocker(t, "boom")
		err := Driver{}.Run("demo-abc", []string{"false"})
		assertBareExit(t, err)
	})

	t.Run("Exec returns bare ExitError", func(t *testing.T) {
		fakeDocker(t, "boom")
		_, err := Driver{}.Exec("demo-abc", []string{"false"})
		assertBareExit(t, err)
	})

	t.Run("Up wraps with subprocess output", func(t *testing.T) {
		fakeDocker(t, "detonated")
		_, err := Driver{}.Up(sandbox.Spec{Name: "demo"})
		assertWrapped(t, err, "detonated")
	})

	t.Run("RemoveImage wraps with subprocess output", func(t *testing.T) {
		fakeDocker(t, "detonated")
		err := Driver{}.RemoveImage("demo")
		assertWrapped(t, err, "detonated")
	})
}

func assertBareExit(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("want a bare *exec.ExitError, got %T: %v", err, err)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error not errors.As-able to *exec.ExitError: %v", err)
	}
}

func assertWrapped(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if _, ok := err.(*exec.ExitError); ok {
		t.Fatalf("want a wrapped driver error, got a bare *exec.ExitError: %v", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not carry subprocess output %q", err.Error(), want)
	}
}

// captureDocker swaps the command seam for one that records every docker argv
// and succeeds, so Up's argument construction can be asserted directly.
func captureDocker(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := command
	command = func(_ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("sh", "-c", "exit 0")
	}
	t.Cleanup(func() { command = orig })
	return &calls
}

// CONTRACT: egress none runs the container with no network; open egress puts
// no --network flag on the argv at all.
func TestUpEgressNone(t *testing.T) {
	t.Run("none sets --network none", func(t *testing.T) {
		calls := captureDocker(t)
		if _, err := (Driver{}).Up(sandbox.Spec{Name: "img", Workdir: "/work", Egress: sandbox.EgressNone}); err != nil {
			t.Fatal(err)
		}
		run := strings.Join((*calls)[0], " ")
		if !strings.Contains(run, "--network none") {
			t.Fatalf("egress none argv missing --network none: %s", run)
		}
	})

	t.Run("open sets no --network", func(t *testing.T) {
		calls := captureDocker(t)
		if _, err := (Driver{}).Up(sandbox.Spec{Name: "img", Workdir: "/work"}); err != nil {
			t.Fatal(err)
		}
		for _, a := range (*calls)[0] {
			if a == "--network" {
				t.Fatalf("open egress must not set --network: %v", (*calls)[0])
			}
		}
	})
}

// CONTRACT: egress proxy = no network + the forwarder binary mounted read-only
// at its fixed in-box path + the keep-alive bracketed by the forwarder, aimed
// at the box door. Proxy env is the actions layer's job, so the driver just
// passes Spec.Env through; the door itself arrives in Sockets like any other.
func TestUpEgressProxy(t *testing.T) {
	calls := captureDocker(t)
	_, err := (Driver{}).Up(sandbox.Spec{
		Name: "img", Workdir: "/work",
		Egress:       sandbox.EgressProxy,
		ForwarderBin: "/host/.dabs/forward",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := strings.Join((*calls)[0], " ")
	for _, want := range []string{
		"--network none",
		"source=/host/.dabs/forward,target=/run/dabs/forward,readonly",
		"/run/dabs/forward /run/dabs/door.sock 18080 -- sleep infinity",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("proxy argv missing %q: %s", want, run)
		}
	}
}

// CONTRACT: every socket in the spec is bound into the box at its own path,
// whatever the egress mode — a proxied box's sockets (its door among them)
// bind exactly as an open box's do.
func TestUpSockets(t *testing.T) {
	t.Run("each socket is bound", func(t *testing.T) {
		calls := captureDocker(t)
		_, err := (Driver{}).Up(sandbox.Spec{
			Name: "img", Workdir: "/work",
			Sockets: []sandbox.Mount{
				{Host: "/host/one.sock", Path: "/run/dabs/one.sock"},
				{Host: "/host/two.sock", Path: "/run/dabs/two.sock"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		run := strings.Join((*calls)[0], " ")
		// The SHORT form is load-bearing: Docker Desktop relays a socket bind
		// written as -v and refuses the same bind written as --mount.
		for _, want := range []string{
			"-v /host/one.sock:/run/dabs/one.sock",
			"-v /host/two.sock:/run/dabs/two.sock",
		} {
			if !strings.Contains(run, want) {
				t.Errorf("argv missing %q: %s", want, run)
			}
		}
		if strings.Contains(run, "target=/run/dabs/one.sock") {
			t.Errorf("socket bound with --mount, which does not carry a socket: %s", run)
		}
		// A socket the box must connect to cannot be bound read-only.
		if strings.Contains(run, "/run/dabs/one.sock:ro") {
			t.Errorf("socket bound read-only: %s", run)
		}
	})

	t.Run("proxy egress binds its sockets like any other box", func(t *testing.T) {
		calls := captureDocker(t)
		_, err := (Driver{}).Up(sandbox.Spec{
			Name: "img", Workdir: "/work",
			Sockets:      []sandbox.Mount{{Host: "/host/one.sock", Path: "/run/dabs/one.sock"}},
			Egress:       sandbox.EgressProxy,
			ForwarderBin: "/host/.dabs/forward",
		})
		if err != nil {
			t.Fatal(err)
		}
		run := strings.Join((*calls)[0], " ")
		for _, want := range []string{
			"-v /host/one.sock:/run/dabs/one.sock",
			"source=/host/.dabs/forward,target=/run/dabs/forward,readonly",
			"/run/dabs/forward /run/dabs/door.sock 18080 -- sleep infinity",
		} {
			if !strings.Contains(run, want) {
				t.Errorf("argv missing %q: %s", want, run)
			}
		}
	})

	t.Run("a spec with no sockets binds none", func(t *testing.T) {
		calls := captureDocker(t)
		if _, err := (Driver{}).Up(sandbox.Spec{Name: "img", Workdir: "/work"}); err != nil {
			t.Fatal(err)
		}
		run := strings.Join((*calls)[0], " ")
		if strings.Contains(run, "type=bind") || strings.Contains(run, "-v ") {
			t.Errorf("a socketless, mountless box bound something: %s", run)
		}
	})
}

// CONTRACT: a box that is both non-root and bound a socket is given group 0,
// and a box missing either half is not.
//
// Docker Desktop relays a -v socket bind through its VM and the socket appears
// inside the container owned root:root with mode 0660, so without group 0 a
// --user box gets EACCES on connect and every socket the recipe declared — the
// box door among them — is unreachable.
func TestUpSocketsForANonRootBox(t *testing.T) {
	t.Run("a user and a socket get group 0", func(t *testing.T) {
		calls := captureDocker(t)
		_, err := (Driver{}).Up(sandbox.Spec{
			Name: "img", Workdir: "/work", User: "1000:1000",
			Sockets: []sandbox.Mount{{Host: "/host/one.sock", Path: "/run/dabs/one.sock"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		run := strings.Join((*calls)[0], " ")
		if !strings.Contains(run, "--group-add 0") {
			t.Fatalf("argv missing --group-add 0: %s", run)
		}
		// Before the image name, or docker reads it as an argument to the
		// container's own command instead of a flag of its own.
		if i, j := indexOf((*calls)[0], "--group-add"), indexOf((*calls)[0], imageName("img")); i > j {
			t.Fatalf("--group-add comes after the image (%d > %d): %s", i, j, run)
		}
	})

	t.Run("a user with no socket gets none", func(t *testing.T) {
		calls := captureDocker(t)
		if _, err := (Driver{}).Up(sandbox.Spec{Name: "img", Workdir: "/work", User: "1000:1000"}); err != nil {
			t.Fatal(err)
		}
		if i := indexOf((*calls)[0], "--group-add"); i >= 0 {
			t.Fatalf("a box with no socket was given a group anyway: %v", (*calls)[0])
		}
	})

	t.Run("a socket with no user gets none", func(t *testing.T) {
		calls := captureDocker(t)
		_, err := (Driver{}).Up(sandbox.Spec{
			Name: "img", Workdir: "/work",
			Sockets: []sandbox.Mount{{Host: "/host/one.sock", Path: "/run/dabs/one.sock"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if i := indexOf((*calls)[0], "--group-add"); i >= 0 {
			t.Fatalf("a box running as the image's own user was given a group anyway: %v", (*calls)[0])
		}
	})
}

// CONTRACT: a spec that names a user runs the container as it, and a spec that
// names none leaves the image's own user alone.
//
// This is the one flag that decides who owns what the box writes: this daemon
// is root, its binds pass the uid straight through, and without --user every
// file the box leaves on a run mount is root-owned and unreadable to the
// unprivileged program that booted it.
func TestUpRunsAsTheSpecsUser(t *testing.T) {
	t.Run("a named user reaches docker run", func(t *testing.T) {
		calls := captureDocker(t)
		if _, err := (Driver{}).Up(sandbox.Spec{Name: "img", Workdir: "/work", User: "1000:1000"}); err != nil {
			t.Fatal(err)
		}
		run := strings.Join((*calls)[0], " ")
		if !strings.Contains(run, "--user 1000:1000") {
			t.Fatalf("argv missing --user 1000:1000: %s", run)
		}
		// Before the image name, or docker reads it as an argument to the
		// container's own command instead of a flag of its own.
		if i, j := indexOf((*calls)[0], "--user"), indexOf((*calls)[0], imageName("img")); i > j {
			t.Fatalf("--user comes after the image (%d > %d): %s", i, j, run)
		}
	})

	t.Run("no user leaves the image's own", func(t *testing.T) {
		calls := captureDocker(t)
		if _, err := (Driver{}).Up(sandbox.Spec{Name: "img", Workdir: "/work"}); err != nil {
			t.Fatal(err)
		}
		for _, a := range (*calls)[0] {
			if a == "--user" {
				t.Fatalf("a spec naming no user set --user anyway: %v", (*calls)[0])
			}
		}
	})
}

// indexOf is where arg sits in argv, or -1.
func indexOf(argv []string, arg string) int {
	for i, a := range argv {
		if a == arg {
			return i
		}
	}
	return -1
}

// TestLiveNonRootBoxConnectsToABoundSocket boots a REAL container through the
// driver and connects from inside it, as the spec's user, to a socket a program
// on the host is listening on. It is the only proof of the group-0 rule: the
// permissions the box sees are invented by Docker Desktop's relay, so no argv
// assertion can show whether the connect succeeds.
//
// Gated on docker being on PATH and its daemon answering, and skipped when the
// test runs as root, whose connect would succeed either way.
func TestLiveNonRootBoxConnectsToABoundSocket(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; a root box connects whatever its groups")
	}

	// A SHORT host path: a unix socket's address is capped at 104 bytes
	// (sun_path), and Docker Desktop answers a longer one by binding an empty
	// directory in its place. t.TempDir() spells the test's name into the path,
	// so the socket gets a directory of its own instead.
	dir, err := os.MkdirTemp("", "dbs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	if len(sock) > 100 {
		t.Skipf("temp dir leaves no room under the 104-byte sun_path cap: %s", sock)
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// 0777 on the host, so a failure to connect is the relay's doing and not
	// this socket's mode. Docker Desktop presents it as root:root 0660 anyway.
	if err := os.Chmod(sock, 0o777); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "pong")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	// The image is built through the driver's own Build, from the same base and
	// the same curl the bundled shell recipe uses (images/shell/Dockerfile), so
	// the box has a client that speaks unix sockets.
	ctx := t.TempDir()
	dockerfile := filepath.Join(ctx, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM alpine:3.20\nRUN apk add --no-cache curl\nWORKDIR /work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := New()
	if err != nil {
		t.Skipf("docker driver unavailable: %v", err)
	}
	const image = "gid0probe"
	if err := d.Build(sandbox.BuildSpec{Name: image, Dockerfile: dockerfile, Context: ctx}); err != nil {
		t.Fatalf("build probe image: %v", err)
	}
	t.Cleanup(func() { _ = d.RemoveImage(image) })

	instance, err := d.Up(sandbox.Spec{
		Name:    image,
		Workdir: "/work",
		User:    fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Sockets: []sandbox.Mount{{Host: sock, Path: "/run/dabs/probe.sock"}},
	})
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	t.Cleanup(func() { _ = d.Down(instance) })

	// docker exec runs as the container's own user, so this connect is the
	// unprivileged one the box's programs make.
	who, err := d.Exec(instance, []string{"id"})
	if err != nil {
		t.Fatalf("id in box: %v", err)
	}
	t.Logf("box identity: %s", strings.TrimSpace(who))

	out, err := d.Exec(instance, []string{"curl", "-sS", "--unix-socket", "/run/dabs/probe.sock", "http://localhost/ping"})
	if err != nil {
		t.Fatalf("connect to the bound socket from inside the box failed: %v\n%s\nbox identity: %s", err, out, who)
	}
	if !strings.Contains(out, "pong") {
		t.Fatalf("connected but read %q, want pong", out)
	}
}
