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

// keysOf lists a map's keys for a failure message.
func keysOf(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
