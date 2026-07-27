package sandbox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/sandbox"
)

// CONTRACT: Lazy defers the constructor (and its vendor-CLI probe) to first
// use — Kind() alone must never build the driver — and once a method is
// called, a failing constructor's own error (the install hint) is what the
// caller gets, from every method, built exactly once.
func TestLazyDefersConstructionToFirstUse(t *testing.T) {
	builds := 0
	drv := sandbox.Lazy("bwrap", func() (sandbox.Driver, error) {
		builds++
		return nil, errors.New("'bwrap' not found; install: apt install bubblewrap")
	})

	if got := drv.Kind(); got != "bwrap" {
		t.Fatalf("Kind() = %q, want bwrap", got)
	}
	if builds != 0 {
		t.Fatalf("Kind() built the driver (%d builds); construction must wait for first real use", builds)
	}

	if _, err := drv.Ls(); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Ls should surface the constructor's install hint, got %v", err)
	}
	if err := drv.Run("x", []string{"ls"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Run should surface the constructor's install hint, got %v", err)
	}
	if builds != 1 {
		t.Fatalf("constructor ran %d times, want exactly once", builds)
	}
}

// --- capability forwarding ----------------------------------------------------

// fakeInner is a driver that can do everything a driver may optionally do. Only
// the capability methods carry behaviour; the rest satisfy sandbox.Driver.
type fakeInner struct {
	detached [][]string
	kind     string
}

func (*fakeInner) Build(sandbox.BuildSpec) error         { return nil }
func (*fakeInner) HasImage(string) (bool, error)         { return true, nil }
func (*fakeInner) Up(sandbox.Spec) (string, error)       { return "inst", nil }
func (*fakeInner) Run(string, []string) error            { return nil }
func (*fakeInner) Exec(string, []string) (string, error) { return "", nil }
func (*fakeInner) Down(string) error                     { return nil }
func (*fakeInner) Ls() ([]sandbox.Info, error)           { return nil, nil }
func (f *fakeInner) Kind() string                        { return f.kind }
func (*fakeInner) CheckDetach() error                    { return nil }
func (f *fakeInner) Detach(_ string, cmd []string) error {
	f.detached = append(f.detached, cmd)
	return nil
}

// plainInner is a driver with no optional capabilities at all — the shape of a
// driver that genuinely cannot hold a detached command.
type plainInner struct{ sandbox.Driver }

func (plainInner) Kind() string { return "plain" }

// CONTRACT: a capability is reached by type-asserting whatever the caller HOLDS,
// and what a caller holds is the wrapper. A wrapped driver that can detach must
// therefore be seen as a Detacher, answer CheckDetach with the INNER driver's
// answer, and pass the command through. This is the bug that shipped: the
// wrapper did not forward Detach, so a driver that could detach was refused with
// a reason that was false for it.
func TestLazyForwardsDetachToTheInnerDriver(t *testing.T) {
	inner := &fakeInner{kind: "apple"}
	drv := sandbox.Lazy("apple", func() (sandbox.Driver, error) { return inner, nil })

	dt, ok := drv.(sandbox.Detacher)
	if !ok {
		t.Fatal("a wrapped driver is not a Detacher; every lazily-wrapped driver would be refused")
	}
	if err := dt.CheckDetach(); err != nil {
		t.Fatalf("CheckDetach on a driver that CAN detach refused: %v", err)
	}
	if err := dt.Detach("inst", []string{"serve"}); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if len(inner.detached) != 1 || inner.detached[0][0] != "serve" {
		t.Fatalf("the command did not reach the inner driver: %v", inner.detached)
	}
}

// CONTRACT: the wrapper must not INVENT a capability either. Wrapping a driver
// that cannot detach still refuses — naming the driver, and claiming no cause
// the wrapper cannot know.
func TestLazyRefusesDetachForAnInnerDriverThatCannot(t *testing.T) {
	drv := sandbox.Lazy("plain", func() (sandbox.Driver, error) { return plainInner{}, nil })

	dt, ok := drv.(sandbox.Detacher)
	if !ok {
		t.Fatal("the wrapper must always be a Detacher; it answers for what it wraps")
	}
	err := dt.CheckDetach()
	if err == nil {
		t.Fatal("CheckDetach claimed a driver with no such capability can detach")
	}
	if !strings.Contains(err.Error(), "plain") {
		t.Errorf("refusal %q should name the driver", err)
	}
	if strings.Contains(err.Error(), "no process of its own") {
		t.Errorf("refusal %q states a cause the wrapper cannot know", err)
	}
	if derr := dt.Detach("inst", []string{"serve"}); derr == nil {
		t.Error("Detach returned nil for a driver that cannot detach; nobody started the command")
	}
}

// CONTRACT: the wrapper stays lazy. Asking about a capability is a real use, so
// it may build the driver — but merely being wrapped must not.
func TestLazyCapabilityQueryDoesNotBuildUntilAsked(t *testing.T) {
	builds := 0
	drv := sandbox.Lazy("apple", func() (sandbox.Driver, error) {
		builds++
		return &fakeInner{kind: "apple"}, nil
	})
	if _, ok := drv.(sandbox.Detacher); !ok {
		t.Fatal("not a Detacher")
	}
	if builds != 0 {
		t.Fatalf("the type assertion built the driver (%d builds)", builds)
	}
	if err := drv.(sandbox.Detacher).CheckDetach(); err != nil {
		t.Fatalf("CheckDetach: %v", err)
	}
	if builds != 1 {
		t.Fatalf("constructor ran %d times, want exactly once", builds)
	}
}
