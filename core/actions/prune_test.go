package actions_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// imgDriver is a fakeDriver that also keeps a local image store, so it satisfies
// the optional sandbox.ImageStore capability the prune action reaches for.
type imgDriver struct {
	*fakeDriver
	imgs      []sandbox.Image
	removed   []string
	removeErr error
}

func (d *imgDriver) Images() ([]sandbox.Image, error) { return d.imgs, nil }
func (d *imgDriver) RemoveImage(name string) error {
	d.removed = append(d.removed, name)
	return d.removeErr
}

func realWithDriver(drv sandbox.Driver) actions.Real {
	return actions.New(map[string]sandbox.Driver{"local": drv}, []string{"local"}, fstest.MapFS{}, baseData())
}

// A driver with an image store: `prune --dry` lists each image and removes
// nothing; `prune` asks the store to remove each one.
func TestPruneListsAndRemoves(t *testing.T) {
	d := &imgDriver{fakeDriver: &fakeDriver{}, imgs: []sandbox.Image{{Name: "alpha", Size: 2048}, {Name: "beta"}}}
	r := realWithDriver(d)

	out := captureStdout(t, func() {
		if err := r.Prune(params.Prune{Dry: true}); err != nil {
			t.Fatalf("Prune --dry: %v", err)
		}
	})
	for _, want := range []string{"alpha", "beta", "2.0 KB"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry list missing %q; got:\n%s", want, out)
		}
	}
	if len(d.removed) != 0 {
		t.Errorf("--dry must not remove anything; removed %v", d.removed)
	}

	out = captureStdout(t, func() {
		if err := r.Prune(params.Prune{}); err != nil {
			t.Fatalf("Prune: %v", err)
		}
	})
	if strings.Join(d.removed, ",") != "alpha,beta" {
		t.Errorf("prune must remove every image; removed %v", d.removed)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("prune output should report removals; got:\n%s", out)
	}
}

// A driver with no local image store (no ImageStore) is skipped, not an error.
func TestPruneSkipsDriverWithoutStore(t *testing.T) {
	r := realWithDriver(&fakeDriver{})
	out := captureStdout(t, func() {
		if err := r.Prune(params.Prune{}); err != nil {
			t.Fatalf("Prune: %v", err)
		}
	})
	if !strings.Contains(out, "no built images") {
		t.Errorf("want 'no built images'; got:\n%s", out)
	}
}

// E2-26: an image a LIVE box was born from ("<image>-<hex12>") is NOT removed
// by a plain prune — it is kept and the blocking box is named. With --force it
// is removed anyway.
func TestPruneKeepsImageWithLiveBox(t *testing.T) {
	newDriver := func() *imgDriver {
		return &imgDriver{
			fakeDriver: &fakeDriver{infos: []sandbox.Info{{Name: "alpha-0011223344ff"}}},
			imgs:       []sandbox.Image{{Name: "alpha"}, {Name: "beta"}},
		}
	}

	d := newDriver()
	out := captureStdout(t, func() {
		if err := realWithDriver(d).Prune(params.Prune{}); err != nil {
			t.Fatalf("Prune: %v", err)
		}
	})
	if strings.Join(d.removed, ",") != "beta" {
		t.Errorf("prune must keep alpha (live box) and remove only beta; removed %v", d.removed)
	}
	if !strings.Contains(out, "alpha kept") || !strings.Contains(out, "alpha-0011223344ff") {
		t.Errorf("prune should report alpha kept and name the blocking box; got:\n%s", out)
	}

	d = newDriver()
	if err := realWithDriver(d).Prune(params.Prune{Force: true}); err != nil {
		t.Fatalf("Prune --force: %v", err)
	}
	if strings.Join(d.removed, ",") != "alpha,beta" {
		t.Errorf("--force must remove even a live-box image; removed %v", d.removed)
	}
}

var _ fs.FS = fstest.MapFS{}
