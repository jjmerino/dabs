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
// the optional sandbox.ImageStore capability the images action reaches for.
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

// A driver with an image store: `images` lists each image; `images prune` asks
// the store to remove each one.
func TestImagesListsAndPrunes(t *testing.T) {
	d := &imgDriver{fakeDriver: &fakeDriver{}, imgs: []sandbox.Image{{Name: "alpha", Size: 2048}, {Name: "beta"}}}
	r := realWithDriver(d)

	out := captureStdout(t, func() {
		if err := r.Images(params.Images{}); err != nil {
			t.Fatalf("Images: %v", err)
		}
	})
	for _, want := range []string{"alpha", "beta", "2.0 KB"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q; got:\n%s", want, out)
		}
	}
	if len(d.removed) != 0 {
		t.Errorf("list must not remove anything; removed %v", d.removed)
	}

	out = captureStdout(t, func() {
		if err := r.Images(params.Images{Prune: true}); err != nil {
			t.Fatalf("Images prune: %v", err)
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
func TestImagesSkipsDriverWithoutStore(t *testing.T) {
	r := realWithDriver(&fakeDriver{})
	out := captureStdout(t, func() {
		if err := r.Images(params.Images{}); err != nil {
			t.Fatalf("Images: %v", err)
		}
	})
	if !strings.Contains(out, "no built images") {
		t.Errorf("want 'no built images'; got:\n%s", out)
	}
}

var _ fs.FS = fstest.MapFS{}
