package actions_test

// Component tests for `dabs build` and the `dabs recipe --no-command`/`--detach`
// boots, which resolve a RECIPE
// (the manifest is gone) and reuse the recipe engine. Driven through the public
// API with the sandbox.Driver and data.Data seams faked; assertions are from
// the CONTRACT, not the implementation.

import (
	"encoding/json"
	"errors"
	"reflect"
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

// CONTRACT: `dabs recipe --no-command` brings up a box (image, env, workdir) and, unlike
// `dabs recipe`, does NOT run the recipe's command and does NOT tear it down.
func TestUpBringsUpBoxNoCommandNoDown(t *testing.T) {
	y := `default: base
recipes:
  base:
    image: img
    workdir: /w
    env: { E2E: "yes" }
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{NoCommand: true}); err != nil {
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
	if len(drv.detached) != 0 {
		t.Errorf("--no-command started the command in the background: %v", drv.detached)
	}
}

// CONTRACT: `dabs recipe --detach` boots a box AND starts the recipe's OWN
// command inside it in the background: the command reaches the driver's Detach
// (the non-blocking start), never its Run (the call that streams and waits), and
// the box is left up.
func TestDetachStartsRecipeCommandInBackgroundAndKeepsBox(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [sh, -c, "while true; do sleep 1; done"]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true}); err != nil {
		t.Fatalf("Recipe --detach: %v", err)
	}
	up := onlyUp(t, drv)
	if len(drv.detached) != 1 {
		t.Fatalf("detached starts = %v, want exactly one", drv.detached)
	}
	got := drv.detached[0]
	if got.instance != up.Name+"-inst" {
		t.Errorf("started the command in %q, want the box just booted (%s-inst)", got.instance, up.Name)
	}
	want := []string{"sh", "-c", "while true; do sleep 1; done"}
	if !reflect.DeepEqual(got.cmd, want) {
		t.Errorf("detached command = %v, want the recipe's own %v", got.cmd, want)
	}
	// Run is the streaming call that waits for the command to exit. A detached
	// boot must never take it, or it would hold the caller for the command's life.
	if len(drv.runs) != 0 {
		t.Errorf("--detach waited on the command through Run: %v", drv.runs)
	}
	if len(drv.downs) != 0 {
		t.Errorf("--detach tore the box down: %v", drv.downs)
	}
}

// CONTRACT: a detached command's output must be readable from the HOST and must
// die with the node — so the box node's OWN tmp space is bound at the box's log
// dir, and the log file named inside it. Nothing about it depends on the box
// still existing.
func TestDetachBindsTheNodesOwnDirectoryAsTheLogDir(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [serve]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true}); err != nil {
		t.Fatalf("Recipe --detach: %v", err)
	}
	nodeID := boxNodeIDFrom(t, fd)
	wantHost := fd.home + "/.dabs/nodes/" + nodeID + "/tmp"
	up := onlyUp(t, drv)
	found := false
	for _, m := range up.Mounts {
		if m.Path == sandbox.DetachedLogDir {
			found = true
			if m.Host != wantHost {
				t.Errorf("log dir bound from %q, want the node's own tmp space %q", m.Host, wantHost)
			}
			if m.RO {
				t.Error("the log dir is bound read-only; the command could not write its output")
			}
		}
	}
	if !found {
		t.Errorf("no mount at %s; the detached log would be trapped in the box: %+v", sandbox.DetachedLogDir, up.Mounts)
	}
}

// CONTRACT: `--no-command` starts nothing, so it binds no log dir — the box gets
// exactly what its recipe declared.
func TestNoCommandBindsNoLogDir(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [serve]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{NoCommand: true}); err != nil {
		t.Fatalf("Recipe --no-command: %v", err)
	}
	for _, m := range onlyUp(t, drv).Mounts {
		if m.Path == sandbox.DetachedLogDir {
			t.Errorf("--no-command bound a detached log dir: %+v", m)
		}
	}
}

// CONTRACT: a recipe with `keep: false` is no different under `--detach`. keep
// decides the fate of a box dabs is WAITING on; nothing waits on a detached one,
// so the box stays up with its command running and is the caller's to reap.
func TestDetachKeepsBoxRegardlessOfRecipeKeep(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    keep: false
    command: [serve]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	if err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true}); err != nil {
		t.Fatalf("Recipe --detach: %v", err)
	}
	if len(drv.downs) != 0 {
		t.Errorf("a keep:false recipe tore the detached box down: %v", drv.downs)
	}
	if len(drv.detached) != 1 {
		t.Fatalf("detached starts = %v, want exactly one", drv.detached)
	}
}

// CONTRACT: `--detach` on a recipe that declares no command refuses — there is
// nothing to start, and `--no-command` is the flag that means "boot, run
// nothing". It must not silently boot a bare box the caller believes is working,
// so nothing is brought up at all.
func TestDetachRefusesRecipeWithNoCommand(t *testing.T) {
	y := `default: bare
recipes:
  bare:
    image: img
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true})
	if err == nil {
		t.Fatal("--detach on a commandless recipe succeeded; want a refusal")
	}
	if !strings.Contains(err.Error(), "no command to run") || !strings.Contains(err.Error(), "--no-command") {
		t.Errorf("error %q should say there is no command and point at --no-command", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("a refused --detach still booted a box: %v", drv.ups)
	}
}

// CONTRACT: a driver whose box has no process of its own cannot hold a detached
// command. `--detach` refuses BEFORE anything is provisioned, naming the driver
// and the flag that does work there — it never degrades into a foreground run.
func TestDetachRefusedByDriverThatCannotHoldOne(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [serve]
`
	fd := baseData()
	inner := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, noDetachDriver{inner}).Recipe(params.Recipe{Detach: true})
	if err == nil {
		t.Fatal("--detach on a driver that cannot detach succeeded; want a refusal")
	}
	if !strings.Contains(err.Error(), "fake") || !strings.Contains(err.Error(), "--no-command") {
		t.Errorf("error %q should name the driver and point at --no-command", err)
	}
	if len(inner.ups) != 0 {
		t.Errorf("a refused --detach still booted a box: %v", inner.ups)
	}
}

// CONTRACT: a driver that HAS the capability but answers that it cannot hold a
// detached command is refused with ITS OWN reason, verbatim — the caller never
// substitutes a cause of its own. A wrapped driver answers for what it wraps, so
// asking is the only way to get this right; a bare type assertion would report
// the wrapper's shape instead of the driver's answer.
func TestDetachRefusedByDriverThatAnswersItCannot(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [serve]
`
	fd := baseData()
	const reason = "the bwrap driver cannot hold a detached command: it enters the box with a fresh bwrap per command"
	drv := &fakeDriver{built: map[string]bool{"img": true}, checkDetachErr: errors.New(reason)}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true})
	if err == nil {
		t.Fatal("--detach succeeded on a driver that answered it cannot; want a refusal")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("error %q should carry the driver's own reason verbatim", err)
	}
	if !strings.Contains(err.Error(), "--no-command") {
		t.Errorf("error %q should point at the flag that does work here", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("a refused --detach still booted a box: %v", drv.ups)
	}
	if len(drv.detached) != 0 {
		t.Errorf("a refused --detach still started the command: %v", drv.detached)
	}
}

// CONTRACT: the refusal for a driver with NO detach capability at all names the
// driver and stops there — it must not claim a mechanical cause the caller
// cannot know (a driver that has no answer has not said why).
func TestDetachRefusalClaimsNoCauseItCannotKnow(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [serve]
`
	fd := baseData()
	inner := &fakeDriver{built: map[string]bool{"img": true}}
	err := newReal(y, fd, noDetachDriver{inner}).Recipe(params.Recipe{Detach: true})
	if err == nil {
		t.Fatal("--detach succeeded on a driver with no detach capability")
	}
	if strings.Contains(err.Error(), "no process of its own") {
		t.Errorf("error %q states a cause the caller cannot know", err)
	}
}

// CONTRACT: a detached command that cannot be STARTED is a boot that did not
// deliver what was asked — the box is reaped rather than left up as an idle
// shell the caller believes is running their command.
func TestDetachReapsBoxWhenTheCommandCannotStart(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [serve]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}, detachErr: errors.New("exec: no /bin/sh in the box")}
	err := newReal(y, fd, drv).Recipe(params.Recipe{Detach: true})
	if err == nil {
		t.Fatal("a failed detached start reported success")
	}
	if !strings.Contains(err.Error(), "no /bin/sh in the box") {
		t.Errorf("error %q should carry the driver's reason", err)
	}
	if len(drv.downs) != 1 {
		t.Errorf("downs = %v, want the unusable box reaped exactly once", drv.downs)
	}
}

// CONTRACT: a detached boot's output must say the command is RUNNING (the one
// thing that differs from --no-command), and hand over where its output went —
// a detached command has no terminal to print to. It must NOT offer the recipe's
// command as something to run: it already is.
func TestDetachOutputSaysRunningAndWhereTheOutputWent(t *testing.T) {
	y := `default: server
recipes:
  server:
    image: img
    command: [sh, -c, "serve --port 80"]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	r := newReal(y, fd, drv)
	out := captureStdout(t, func() {
		if err := r.Recipe(params.Recipe{Detach: true}); err != nil {
			t.Fatalf("Recipe --detach: %v", err)
		}
	})
	nodeID := boxNodeIDFrom(t, fd)
	logFile := fd.home + "/.dabs/nodes/" + nodeID + "/tmp/" + sandbox.DetachedLogName
	for _, want := range []string{
		"recipe booted: server",
		"id: " + nodeID,
		"detached, running: sh -c 'serve --port 80'",
		"reap: dabs rm " + nodeID,
		"output: tail -f " + logFile,
		"sh in: dabs exec " + nodeID + " -- sh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detach output missing %q; got:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"no command was run", "run recipe command:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("detach output should not contain %q; got:\n%s", unwanted, out)
		}
	}
}

// CONTRACT: `dabs recipe --no-command` prepares a recipe's sources exactly as `dabs recipe` does
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
	if err := newReal(y, fd, drv).Recipe(params.Recipe{NoCommand: true}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	up := onlyUp(t, drv)
	if ms := sourceMounts(up.Mounts); len(ms) != 1 || ms[0] != (sandbox.Mount{Host: "/data", Path: "/work"}) {
		t.Errorf("Up mounts = %+v, want one {/data -> /work}", ms)
	}
}

// CONTRACT: a recipe's `target` routes `dabs recipe --no-command`'s box to that fleet driver —
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
	if err := r.Recipe(params.Recipe{NoCommand: true}); err != nil {
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
	if err := r.Recipe(params.Recipe{NoCommand: true}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(remote.ups) != 1 || remote.ups[0].Name != "m" {
		t.Fatalf("want remote Up of image %q, got %+v", "m", remote.ups)
	}
}

// CONTRACT: `dabs recipe --no-command` must NOT build the recipe's own Dockerfile locally — it
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
	err := newReal(y, fd, drv).Recipe(params.Recipe{NoCommand: true})
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

// CONTRACT: `dabs recipe --no-command`'s output must be self-explanatory: the instance is named
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
		if err := r.Recipe(params.Recipe{NoCommand: true}); err != nil {
			t.Fatalf("Up: %v", err)
		}
	})
	// The canonical handle is the NODE ID (recipe-prefixed), not the instance;
	// the instance is kept on its own line, and the hint lines use the node id.
	nodeID := boxNodeIDFrom(t, fd)
	for _, want := range []string{
		"recipe booted: review",
		"id: " + nodeID,
		"instance: img-inst",
		"no command was run",
		"reap: dabs rm " + nodeID,
		"sh in: dabs exec " + nodeID + " -- sh",
		`run recipe command: dabs exec ` + nodeID + ` -- sh -c 'cd /work && claude -p '\''go'\'''`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("up output missing %q; got:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(nodeID, "review-") {
		t.Errorf("node id %q should be recipe-prefixed", nodeID)
	}
}

// boxNodeIDFrom returns the id of the box node the fake driver's `--no-command` run
// wrote — minted fresh each run, so a test reads it back rather than hardcoding.
func boxNodeIDFrom(t *testing.T, fd *fakeData) string {
	t.Helper()
	for path, data := range fd.files {
		if !strings.HasSuffix(path, "dabs-node.json") {
			continue
		}
		if strings.Contains(string(data), `"kind": "box"`) {
			var n struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(data, &n); err == nil && n.Kind == "box" {
				return n.ID
			}
		}
	}
	t.Fatalf("no box node written")
	return ""
}

// CONTRACT: when the recipe's command IS exactly `sh`, the "run recipe command"
// line already renders `dabs exec <inst> -- sh`, so the "sh in:" line would
// repeat it under a second label — it is dropped, leaving one line.
func TestUpOutputShCommandDropsRedundantShInLine(t *testing.T) {
	y := `default: box
recipes:
  box:
    image: img
    command: [sh]
`
	fd := baseData()
	drv := &fakeDriver{built: map[string]bool{"img": true}}
	r := newReal(y, fd, drv)
	out := captureStdout(t, func() {
		if err := r.Recipe(params.Recipe{NoCommand: true}); err != nil {
			t.Fatalf("Up: %v", err)
		}
	})
	if strings.Contains(out, "sh in:") {
		t.Errorf("sh in: line should be dropped when command is sh; got:\n%s", out)
	}
	nodeID := boxNodeIDFrom(t, fd)
	if !strings.Contains(out, "run recipe command: dabs exec "+nodeID+" -- sh") {
		t.Errorf("run recipe command line missing; got:\n%s", out)
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
		if err := r.Recipe(params.Recipe{NoCommand: true}); err != nil {
			t.Fatalf("Up: %v", err)
		}
	})
	if !strings.Contains(out, "run recipe command: (this recipe declares no command)") {
		t.Errorf("want the commandless note; got:\n%s", out)
	}
}
