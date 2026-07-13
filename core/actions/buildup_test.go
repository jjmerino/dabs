package actions_test

// Component tests for `dabs build` and `dabs up`, which now resolve a RECIPE
// (the manifest is gone) and reuse the recipe engine. Driven through the public
// API with the sandbox.Driver and data.Data seams faked; assertions are from
// the CONTRACT, not the implementation.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jjmerino/dabs/core/actions"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// --- build -------------------------------------------------------------------

// CONTRACT: `dabs build` with no name resolves the registry default and builds
// that recipe's image from its inline Dockerfile.
func TestBuildDefaultRecipeBuildsImage(t *testing.T) {
	y := `default: base
recipes:
  base:
    image:
      dockerfile: Dockerfile
      context: .
`
	fd := baseData()
	drv := &fakeDriver{}
	if err := newReal(y, fd, drv).Build(params.Build{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(drv.builds) != 1 || drv.builds[0].Name != "base" {
		t.Fatalf("want one Build of recipe %q, got %+v", "base", drv.builds)
	}
	if len(drv.ups) != 0 {
		t.Errorf("build must not bring a box up: %v", drv.ups)
	}
}

// CONTRACT: `dabs build` FORCES a rebuild of an inline-Dockerfile image even
// when it already exists — that is how an edited Dockerfile is rebuilt. Only
// recipe/up reuse an existing image; build never skips.
func TestBuildForcesRebuildWhenImageExists(t *testing.T) {
	y := `default: base
recipes:
  base:
    image: { dockerfile: Dockerfile, context: . }
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"base": true}} // already built
	if err := newReal(y, fd, drv).Build(params.Build{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(drv.builds) != 1 || drv.builds[0].Name != "base" {
		t.Fatalf("build must force a rebuild even when built, got %+v", drv.builds)
	}
}

// CONTRACT: `dabs build <name>` (a bare recipe name) resolves and builds that
// named recipe's image — the review's blocker was build erroring on a name.
func TestBuildNamedRecipe(t *testing.T) {
	y := `recipes:
  other:
    image: { dockerfile: Dockerfile, context: . }
  chosen:
    image: { dockerfile: Dockerfile, context: . }
`
	fd := baseData()
	drv := &fakeDriver{}
	if err := newReal(y, fd, drv).Build(params.Build{Name: "chosen"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(drv.builds) != 1 || drv.builds[0].Name != "chosen" {
		t.Fatalf("want one Build of %q, got %+v", "chosen", drv.builds)
	}
}

// CONTRACT: `dabs build <bogus>` (not a path, not a known recipe) fails clearly,
// listing what IS known — build/up take a recipe, not a manifest.
func TestBuildUnknownRecipeLists(t *testing.T) {
	y := `recipes:
  known-one:
    image: { dockerfile: Dockerfile, context: . }
`
	fd := baseData()
	drv := &fakeDriver{}
	err := newReal(y, fd, drv).Build(params.Build{Name: "nope"})
	if err == nil || !strings.Contains(err.Error(), `no recipe "nope"`) {
		t.Fatalf("want 'no recipe' error, got %v", err)
	}
	if !strings.Contains(err.Error(), "known-one") {
		t.Fatalf("error should list known recipes, got %v", err)
	}
	if len(drv.builds) != 0 {
		t.Errorf("nothing should have been built: %v", drv.builds)
	}
}

// CONTRACT: `dabs build` with no name and no default errors, listing choices —
// an agent must pick one.
func TestBuildNoDefaultErrors(t *testing.T) {
	fd := baseData()
	drv := &fakeDriver{}
	err := newReal("", fd, drv).Build(params.Build{})
	if err == nil || !strings.Contains(err.Error(), "no default set") {
		t.Fatalf("want 'no default set' error, got %v", err)
	}
}

// CONTRACT: `dabs build <path/to/dabs.yaml>` loads that file and builds its
// default recipe, resolving the inline Dockerfile/context relative to the FILE's
// directory (not the cwd) — the property the server driver's staged recipe and
// `dabs build <dir>` both depend on.
func TestBuildFromDabsYamlPathRebasesBuildPaths(t *testing.T) {
	y := `default: base
recipes:
  base:
    image:
      dockerfile: Dockerfile.dabs
      context: context
`
	fd := baseData()
	path := "/proj/stage/dabs.yaml"
	fd.exists[path] = true
	fd.files = map[string][]byte{path: []byte(y)}
	drv := &fakeDriver{}
	if err := newReal("", fd, drv).Build(params.Build{Name: path}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(drv.builds) != 1 {
		t.Fatalf("want one Build, got %+v", drv.builds)
	}
	b := drv.builds[0]
	if b.Name != "base" || b.Dockerfile != "/proj/stage/Dockerfile.dabs" || b.Context != "/proj/stage/context" {
		t.Errorf("build spec = %+v, want name base, dockerfile /proj/stage/Dockerfile.dabs, context /proj/stage/context", b)
	}
}

// CONTRACT: `dabs build <dir>` resolves the dir's dabs.yaml and builds its
// default recipe, rebasing the inline Dockerfile/context onto the dir (as the
// old manifest-by-dir form did) — `build [recipe|path]` accepts a directory.
func TestBuildFromDabsYamlDirResolvesFile(t *testing.T) {
	y := `default: base
recipes:
  base:
    image:
      dockerfile: Dockerfile
      context: .
`
	fd := baseData()
	dir := "/proj"
	fd.exists[dir] = true
	fd.isDir[dir] = true
	fd.files = map[string][]byte{dir + "/dabs.yaml": []byte(y)}
	drv := &fakeDriver{}
	if err := newReal("", fd, drv).Build(params.Build{Name: dir}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(drv.builds) != 1 {
		t.Fatalf("want one Build, got %+v", drv.builds)
	}
	b := drv.builds[0]
	if b.Name != "base" || b.Dockerfile != "/proj/Dockerfile" || b.Context != "/proj" {
		t.Errorf("build spec = %+v, want name base, dockerfile /proj/Dockerfile, context /proj", b)
	}
}

// CONTRACT: `dabs build` on a bare-image recipe (no Dockerfile) has nothing to
// build — it must say so honestly, not claim "<name> built" for a no-op.
func TestBuildBareImageSaysNothingToBuild(t *testing.T) {
	y := `recipes:
  s:
    image: shell
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"shell": true}}
	out := captureStdout(t, func() {
		if err := newReal(y, fd, drv).Build(params.Build{Name: "s"}); err != nil {
			t.Fatalf("Build: %v", err)
		}
	})
	if strings.Contains(out, "built") {
		t.Errorf("bare-image build claimed a build happened: %q", out)
	}
	if !strings.Contains(out, "nothing to build") || !strings.Contains(out, "shell") {
		t.Errorf("want an honest nothing-to-build message naming the image, got %q", out)
	}
	if len(drv.builds) != 0 {
		t.Errorf("a bare-image build should not build anything: %v", drv.builds)
	}
}

// --- up ----------------------------------------------------------------------

// CONTRACT: `dabs up` brings up a DETACHED box (image, env, workdir) and, unlike
// `dabs recipe`, does NOT run the recipe's command and does NOT tear it down.
func TestUpBringsUpDetachedNoCommandNoDown(t *testing.T) {
	y := `default: base
recipes:
  base:
    image: img
    workdir: /w
    env: { E2E: "yes" }
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Up(params.Up{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	up := onlyUp(t, drv)
	if up.Name != "img" || up.Workdir != "/w" || up.Env["E2E"] != "yes" {
		t.Errorf("Up spec = %+v, want image img workdir /w env E2E=yes", up)
	}
	if len(drv.runs) != 0 {
		t.Errorf("up ran a command: %v", drv.runs)
	}
	if len(drv.downs) != 0 {
		t.Errorf("up tore the box down: %v", drv.downs)
	}
}

// CONTRACT: `dabs up` prepares a recipe's sources exactly as `dabs recipe` does
// — the same declared mount reaches the driver.
func TestUpMountsSourcesLikeRecipe(t *testing.T) {
	y := `default: m
recipes:
  m:
    image: img
    sources:
      - mount: /data
        path: /work
`
	fd := baseData()
	fd.exists["/data"] = true
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Up(params.Up{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	up := onlyUp(t, drv)
	if len(up.Mounts) != 1 || up.Mounts[0] != (sandbox.Mount{Host: "/data", Path: "/work"}) {
		t.Errorf("Up mounts = %+v, want one {/data -> /work}", up.Mounts)
	}
}

// CONTRACT: a recipe's `target` routes `dabs up`'s box to that fleet driver —
// and it works even though a remote/server driver's HasImage returns false BY
// DESIGN (it cannot cheaply probe). The remote fake mirrors that: gating `up` on
// HasImage would have wrongly rejected the remote box (the review's blocker).
func TestUpRoutesToTargetDespiteUnprobableHasImage(t *testing.T) {
	y := `default: m
recipes:
  m:
    image: img
    target: remote
`
	fd := baseData()
	fd.files = map[string][]byte{fd.home + "/.dabs/recipes.yaml": []byte(y)}
	local := &fakeDriver{built: map[string]bool{"img": true}}
	remote := &fakeDriver{} // like the server driver: HasImage → false always
	r := actions.New(
		map[string]sandbox.Driver{"local": local, "remote": remote},
		[]string{"local", "remote"}, fstest.MapFS{}, fd,
	)
	if err := r.Up(params.Up{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(remote.ups) != 1 || len(local.ups) != 0 {
		t.Fatalf("target=remote routed wrong: local ups=%d remote ups=%d", len(local.ups), len(remote.ups))
	}
}

// CONTRACT: a `target` recipe whose image is an inline Dockerfile also boots on
// the remote — the driver ships+builds it (like `dabs build` did), so `up` must
// pass the recipe name straight through instead of gating on the unprobable
// remote HasImage.
func TestUpTargetInlineImageRoutesToRemote(t *testing.T) {
	y := `default: m
recipes:
  m:
    image: { dockerfile: Dockerfile, context: . }
    target: remote
`
	fd := baseData()
	fd.files = map[string][]byte{fd.home + "/.dabs/recipes.yaml": []byte(y)}
	local := &fakeDriver{}
	remote := &fakeDriver{} // HasImage → false, as the server driver reports
	r := actions.New(
		map[string]sandbox.Driver{"local": local, "remote": remote},
		[]string{"local", "remote"}, fstest.MapFS{}, fd,
	)
	if err := r.Up(params.Up{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(remote.ups) != 1 || remote.ups[0].Name != "m" {
		t.Fatalf("want remote Up of image %q, got %+v", "m", remote.ups)
	}
}

// CONTRACT: `dabs up` must NOT build the recipe's own Dockerfile locally — it
// boots what `dabs build` produced. A LOCAL inline-{dockerfile} image that isn't
// built yet fails clearly (pointing at `dabs build`) rather than building
// in-place.
func TestUpUnbuiltInlineImageErrors(t *testing.T) {
	y := `default: base
recipes:
  base:
    image: { dockerfile: Dockerfile, context: . }
`
	fd := baseData()
	drv := &fakeDriver{} // HasImage("base") is false — nothing built yet
	err := newReal(y, fd, drv).Up(params.Up{})
	if err == nil || !strings.Contains(err.Error(), "dabs build") {
		t.Fatalf("want an 'image not built — run dabs build' error, got %v", err)
	}
	if len(drv.builds) != 0 {
		t.Errorf("up must not build: %v", drv.builds)
	}
	if len(drv.ups) != 0 {
		t.Errorf("up brought a box up from an unbuilt image: %v", drv.ups)
	}
}

// CONTRACT: `dabs up`'s output must be self-explanatory: the instance is named
// after the IMAGE, so the line must name the RECIPE too; it must say no command
// was run (up deliberately starts none); and it must hand over the next steps —
// reap, shell in, and how to run the recipe's own command (there is no verb for
// that, so it is spelled out as an exec of the recipe's argv).
func TestUpOutputNamesRecipeSaysNoCommandAndNextSteps(t *testing.T) {
	y := `default: review
recipes:
  review:
    image: img
    command: [sh, -c, "cd /work && claude -p 'go'"]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	r := newReal(y, fd, drv)
	out := captureStdout(t, func() {
		if err := r.Up(params.Up{}); err != nil {
			t.Fatalf("Up: %v", err)
		}
	})
	for _, want := range []string{
		"recipe up: review",
		"id: img-inst",
		"no command was run",
		"bring down: dabs down img-inst",
		"sh in: dabs exec img-inst -- sh",
		`run recipe command: dabs exec img-inst -- sh -c 'cd /work && claude -p '\''go'\'''`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("up output missing %q; got:\n%s", want, out)
		}
	}
}

// CONTRACT: a recipe with no command still gets an honest "run recipe command"
// line — dabs never prints a command that would not work.
func TestUpOutputCommandlessRecipe(t *testing.T) {
	y := `default: base
recipes:
  base:
    image: img
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	r := newReal(y, fd, drv)
	out := captureStdout(t, func() {
		if err := r.Up(params.Up{}); err != nil {
			t.Fatalf("Up: %v", err)
		}
	})
	if !strings.Contains(out, "run recipe command: (this recipe declares no command)") {
		t.Errorf("want the commandless note; got:\n%s", out)
	}
}
