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
