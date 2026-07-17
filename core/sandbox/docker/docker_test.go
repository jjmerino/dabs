package docker

import (
	"errors"
	"os/exec"
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

// CONTRACT: egress proxy = no network + the socket and dabs binary mounted
// read-only at the forwarder's fixed in-box paths + the keep-alive bracketed
// by the forwarder. Proxy env is the actions layer's job, so the driver just
// passes Spec.Env through.
func TestUpEgressProxy(t *testing.T) {
	calls := captureDocker(t)
	_, err := (Driver{}).Up(sandbox.Spec{
		Name: "img", Workdir: "/work",
		Egress: sandbox.EgressProxy, ProxySock: "/host/.dabs/egress.sock",
		ForwarderBin: "/host/.dabs/forward",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := strings.Join((*calls)[0], " ")
	for _, want := range []string{
		"--network none",
		"source=/host/.dabs/egress.sock,target=/run/dabs/egress.sock,readonly",
		"source=/host/.dabs/forward,target=/run/dabs/forward,readonly",
		"/run/dabs/forward /run/dabs/egress.sock 18080 -- sleep infinity",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("proxy argv missing %q: %s", want, run)
		}
	}
}
