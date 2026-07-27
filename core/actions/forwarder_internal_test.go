package actions

// WithForwarder is the library's door to proxy egress: an embedding program has
// no embedded forwarder to fall back on, so the path it hands over must survive
// the option chain and be what a proxy boot provisions with. These tests reach
// inside the package to watch the field itself; the box-level proof that the
// supplied binary is the one mounted lives in the e2e suite.

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/sandbox"
)

func newForwarderReal() Real {
	return New(map[string]sandbox.Driver{}, nil, fs.FS(fstest.MapFS{}), nil)
}

// CONTRACT: absent the option there is no supplied forwarder, so provisioning
// asks for the embedded copy and behaves exactly as it did before the door
// existed — embed if built with the tag, refusal otherwise.
func TestNoForwarderOptionMeansTheEmbeddedCopy(t *testing.T) {
	if got := newForwarderReal().forwarder; got != "" {
		t.Errorf("default forwarder = %q, want empty (use the embed)", got)
	}
}

// CONTRACT: the supplied path is carried on the actions value, so every box a
// caller boots from it provisions with that binary — and, like the other
// options, WithForwarder returns a COPY, leaving the value it was called on for
// callers that want the embed.
func TestWithForwarderCarriesThePathAndCopies(t *testing.T) {
	base := newForwarderReal()
	supplied := base.WithForwarder("/opt/forward")
	if supplied.forwarder != "/opt/forward" {
		t.Errorf("forwarder = %q, want the supplied path", supplied.forwarder)
	}
	if base.forwarder != "" {
		t.Errorf("WithForwarder mutated its receiver: %q", base.forwarder)
	}
}

// CONTRACT: the option composes with the others — chaining does not drop the
// forwarder, and setting the forwarder does not drop the confirm gate.
func TestWithForwarderComposesWithOtherOptions(t *testing.T) {
	r := newForwarderReal().WithForwarder("/opt/forward").WithConfirm(func(string) bool { return true })
	if r.forwarder != "/opt/forward" {
		t.Errorf("forwarder = %q after chaining, want the supplied path", r.forwarder)
	}
	if r.confirm == nil || !r.confirm("") {
		t.Error("chaining lost the confirm gate")
	}
}
