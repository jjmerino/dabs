package actions_test

// Behavioural tests for stale-image detection: an image is reused only when it
// EXISTS and the source it was built from is UNCHANGED. These drive the public
// Real API with the driver and data seams faked, so the reuse-vs-rebuild
// DECISION (and the message that explains it) is asserted deterministically —
// the e2e box has no image builder (bwrap shells out to docker, which the box
// lacks BY DESIGN), so the real build cannot be exercised there.
//
// They are the executable form of the spec:
//   TestChangedDockerfileRebuildsImage   — an edited source rebuilds, and says so
//   TestUnchangedDockerfileSkipsRebuild  — an unchanged source is reused (#39 speedup)
//   TestLegacyImageWithoutRecordRebuildsOnce — a pre-record image self-heals once
// plus the bundled-image variant, which is the owner's actual curl case.

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// inlineBootRecipe is a minimal runnable recipe whose image is an inline
// Dockerfile — the shape a `dabs recipe` boot ensures and rebuilds.
const inlineBootRecipe = `default: base
recipes:
  base:
    image: { dockerfile: Dockerfile, context: . }
    command: [x]
    sources:
      - mount: /data
        path: /work
`

// bootInline boots the inline recipe once and returns what dabs printed.
func bootInline(t *testing.T, fd *fakeData, drv *fakeDriver) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := newReal(inlineBootRecipe, fd, drv).Recipe(params.Recipe{Name: "base"}); err != nil {
			t.Fatalf("boot: %v", err)
		}
	})
}

// TestChangedDockerfileRebuildsImage: a first boot builds the image and records
// its source digest; editing the Dockerfile and booting again must REBUILD (the
// bug: image existence alone let the stale image serve for ever) and SAY why.
func TestChangedDockerfileRebuildsImage(t *testing.T) {
	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{}
	fd.files["/cwd/Dockerfile"] = []byte("FROM alpine\nRUN echo v1 > /marker\n")
	drv := &fakeDriver{} // nothing built yet

	bootInline(t, fd, drv) // v1: builds and records
	if len(drv.builds) != 1 {
		t.Fatalf("first boot should build once, got %d builds", len(drv.builds))
	}

	// Edit the Dockerfile (v2) and boot again.
	fd.files["/cwd/Dockerfile"] = []byte("FROM alpine\nRUN echo v2 > /marker\n")
	out := bootInline(t, fd, drv)

	if len(drv.builds) != 2 {
		t.Fatalf("an edited Dockerfile must rebuild; got %d builds (stale image served)", len(drv.builds))
	}
	if !strings.Contains(out, "Dockerfile changed — rebuilding") {
		t.Fatalf("a rebuild must announce why; got:\n%s", out)
	}
}

// TestUnchangedDockerfileSkipsRebuild: with the Dockerfile untouched, the second
// boot reuses the image — the #39 speedup (don't rebuild a built image) must
// survive the fix.
func TestUnchangedDockerfileSkipsRebuild(t *testing.T) {
	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{}
	fd.files["/cwd/Dockerfile"] = []byte("FROM alpine\nRUN echo v1 > /marker\n")
	drv := &fakeDriver{}

	bootInline(t, fd, drv) // builds once, records
	bootInline(t, fd, drv) // unchanged → reuse

	if len(drv.builds) != 1 {
		t.Fatalf("an unchanged Dockerfile must NOT rebuild; got %d builds", len(drv.builds))
	}
}

// TestLegacyImageWithoutRecordRebuildsOnce: an image built before this change
// exists but has no digest record. It is treated as stale ONCE — rebuilt and
// recorded — then reused; deleting the record makes it rebuild once again. This
// is the owner's curl case self-healing.
func TestLegacyImageWithoutRecordRebuildsOnce(t *testing.T) {
	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{}
	fd.files["/cwd/Dockerfile"] = []byte("FROM alpine\nRUN echo v1 > /marker\n")
	drv := &fakeDriver{built: map[string]bool{"base": true}} // present, no record

	out := bootInline(t, fd, drv)
	if len(drv.builds) != 1 {
		t.Fatalf("a legacy image with no record must rebuild once; got %d builds", len(drv.builds))
	}
	if !strings.Contains(out, "no build record — rebuilding") {
		t.Fatalf("the legacy rebuild must say it had no record; got:\n%s", out)
	}

	bootInline(t, fd, drv) // now recorded → reuse
	if len(drv.builds) != 1 {
		t.Fatalf("after recording, an unchanged image must be reused; got %d builds", len(drv.builds))
	}

	// Simulate a pre-fix machine again: delete the digest record.
	metaPath := "/home/t/.dabs/images-meta/base.json"
	if _, ok := fd.files[metaPath]; !ok {
		t.Fatalf("expected a digest record at %s; files: %v", metaPath, keysOf(fd.files))
	}
	delete(fd.files, metaPath)

	bootInline(t, fd, drv)
	if len(drv.builds) != 2 {
		t.Fatalf("a deleted record must rebuild once more; got %d builds", len(drv.builds))
	}
}

// TestChangedBundledImageRebuilds: the owner's actual case — a BUNDLED image
// (images/shell) whose embedded files change (curl added). The digest covers the
// whole embedded directory, so an already-present bundled image with no record
// self-heals once, is then reused, and rebuilds when an embedded file changes.
func TestChangedBundledImageRebuilds(t *testing.T) {
	dockerfile := &fstest.MapFile{Data: []byte("FROM alpine\nRUN apk add --no-cache git\n")}
	imgs := fstest.MapFS{"images/shell/Dockerfile": dockerfile}

	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{
		fd.home + "/.dabs/recipes.yaml": []byte(`recipes:
  s:
    image: shell
    command: [x]
    sources:
      - mount: /data
        path: /work
`),
	}
	drv := &fakeDriver{built: map[string]bool{"shell": true}} // present, legacy (no record)
	newBundled := func() actions.Real {
		return actions.New(map[string]sandbox.Driver{"local": drv}, []string{"local"}, imgs, fd)
	}

	out := captureStdout(t, func() {
		if err := newBundled().Recipe(params.Recipe{Name: "s"}); err != nil {
			t.Fatalf("boot: %v", err)
		}
	})
	if len(drv.builds) != 1 || !strings.Contains(out, "no build record — rebuilding") {
		t.Fatalf("a legacy bundled image must self-heal once with a reason; builds=%d out=%s", len(drv.builds), out)
	}

	// Unchanged → reuse.
	if err := newBundled().Recipe(params.Recipe{Name: "s"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if len(drv.builds) != 1 {
		t.Fatalf("an unchanged bundled image must be reused; got %d builds", len(drv.builds))
	}

	// An embedded file changes (curl added) → rebuild.
	dockerfile.Data = []byte("FROM alpine\nRUN apk add --no-cache git curl\n")
	if err := newBundled().Recipe(params.Recipe{Name: "s"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if len(drv.builds) != 2 {
		t.Fatalf("a changed embedded file must rebuild the bundled image; got %d builds", len(drv.builds))
	}
}

// TestStagedImageWithoutBuilderIsServedAsIs: a STAGED image — a rootfs placed
// in the image dir by something other than a dabs build (a nested-sandboxing
// box's Dockerfile stages `shell` this way) — exists but has no build record,
// so the freshness check wants a rebuild. On a driver with no builder (bwrap
// without docker), that rebuild can never run: the boot must serve the staged
// image as-is and say freshness was not verified, not fail. This broke every
// nested `dabs recipe sh` boot inside a dabseption/e2e box.
func TestStagedImageWithoutBuilderIsServedAsIs(t *testing.T) {
	dockerfile := &fstest.MapFile{Data: []byte("FROM alpine\nRUN apk add --no-cache git\n")}
	imgs := fstest.MapFS{"images/shell/Dockerfile": dockerfile}
	recipes := []byte(`recipes:
  s:
    image: shell
    command: [x]
    sources:
      - mount: /data
        path: /work
`)

	fd := baseData()
	fd.exists["/data"] = true
	fd.files = map[string][]byte{fd.home + "/.dabs/recipes.yaml": recipes}
	noBuilder := fmt.Errorf("bwrap: 'docker' not found (%w)", sandbox.ErrNoBuilder)
	drv := &fakeDriver{built: map[string]bool{"shell": true}, buildErr: noBuilder} // staged: present, no record, unbuildable

	out := captureStdout(t, func() {
		if err := actions.New(map[string]sandbox.Driver{"local": drv}, []string{"local"}, imgs, fd).Recipe(params.Recipe{Name: "s"}); err != nil {
			t.Fatalf("boot with a staged image on a builder-less driver must serve it, got: %v", err)
		}
	})
	if !strings.Contains(out, "serving it as-is") {
		t.Errorf("serving an unverified image must say so; got:\n%s", out)
	}
	if len(drv.ups) != 1 {
		t.Errorf("the box must boot from the staged image; ups=%v", drv.ups)
	}

	// The fallback is for a rebuild that CANNOT run — any other build failure
	// still fails the boot.
	fd2 := baseData()
	fd2.exists["/data"] = true
	fd2.files = map[string][]byte{fd2.home + "/.dabs/recipes.yaml": recipes}
	drv2 := &fakeDriver{built: map[string]bool{"shell": true}, buildErr: fmt.Errorf("docker build: exit 1")}
	if err := actions.New(map[string]sandbox.Driver{"local": drv2}, []string{"local"}, imgs, fd2).Recipe(params.Recipe{Name: "s"}); err == nil {
		t.Fatal("a failed build (builder present) must fail the boot, not serve the stale image")
	}

	// A record EXISTS and the source CHANGED: the freshness contract demands a
	// rebuild, and with no builder the boot must FAIL — never quietly serve a
	// build known to be stale.
	changing := &fstest.MapFile{Data: []byte("FROM alpine\nRUN apk add --no-cache git\n")}
	imgs4 := fstest.MapFS{"images/shell/Dockerfile": changing}
	fd4 := baseData()
	fd4.exists["/data"] = true
	fd4.files = map[string][]byte{fd4.home + "/.dabs/recipes.yaml": recipes}
	drv4 := &fakeDriver{} // a builder, for the first boot
	if err := actions.New(map[string]sandbox.Driver{"local": drv4}, []string{"local"}, imgs4, fd4).Recipe(params.Recipe{Name: "s"}); err != nil {
		t.Fatalf("first boot (builds and records): %v", err)
	}
	changing.Data = []byte("FROM alpine\nRUN apk add --no-cache git curl\n") // source changes...
	drv4.buildErr = noBuilder                                                // ...and the builder is gone
	if err := actions.New(map[string]sandbox.Driver{"local": drv4}, []string{"local"}, imgs4, fd4).Recipe(params.Recipe{Name: "s"}); err == nil {
		t.Fatal("a recorded image whose source changed must not be served when the rebuild cannot run")
	}

	// No builder AND no image: nothing to serve — the boot fails with the
	// driver's own no-builder error.
	fd3 := baseData()
	fd3.exists["/data"] = true
	fd3.files = map[string][]byte{fd3.home + "/.dabs/recipes.yaml": recipes}
	drv3 := &fakeDriver{buildErr: noBuilder}
	if err := actions.New(map[string]sandbox.Driver{"local": drv3}, []string{"local"}, imgs, fd3).Recipe(params.Recipe{Name: "s"}); err == nil {
		t.Fatal("no builder and no image must fail the boot")
	}
}

// keysOf lists a map's keys for a failure message.
func keysOf(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
