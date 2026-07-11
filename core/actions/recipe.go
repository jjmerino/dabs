package actions

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// Recipe runs the named recipe (no name → the dabs.yaml `default:`). A trailing
// command from `dabs recipe <name> <cmd…>` is appended to the recipe's own
// command and gated behind a look-before-run confirmation.
func (r Real) Recipe(p params.Recipe) error {
	reg, err := r.loadRegistry()
	if err != nil {
		return err
	}
	name := p.Name
	if name == "" {
		if reg.Default == "" {
			return fmt.Errorf("no recipe given and no default set — choose one: %s (or set `default:` in dabs.yaml)", strings.Join(reg.Names(), ", "))
		}
		name = reg.Default
	}
	return r.runRecipe(reg, name, p.Worktree, p.Cmd, false)
}

// Do runs `dabs do <cmd…>`: a throwaway box over the DEFAULT recipe — the
// dabs.yaml `default:` if set, else the bundled generic `sh` box — with the
// command appended. It is the quick "just run this in a sandbox" alias, and it
// ALWAYS confirms first (look-before-run), even with no appended command.
func (r Real) Do(p params.Do) error {
	reg, err := r.loadRegistry()
	if err != nil {
		return err
	}
	name := reg.Default
	if name == "" {
		name = "sh" // no project default → the bundled generic shell box
	}
	return r.runRecipe(reg, name, "", p.Cmd, true)
}

// runRecipe is the shared engine behind `dabs recipe`, `dabs cast`, and
// `dabs do`: it ensures the image exists, prepares the recipe's sources (live
// mounts, fresh git worktrees, and up-time copies), brings up a box with them,
// runs the recipe's command interactively, and tears the box down on exit.
// Worktrees are KEPT (paths printed) so no in-box work is silently discarded.
// Everything the box does is declared in the recipe. `extra` is appended to the
// recipe's command; when it is non-empty the caller must first approve the
// recipe and the exact command (nothing is built or run until they do).
func (r Real) runRecipe(reg recipe.Registry, name, worktree string, extra []string, alwaysConfirm bool) error {
	rec, err := reg.Get(name)
	if err != nil {
		return err
	}
	command := append(append([]string{}, rec.Command...), extra...)
	if len(command) == 0 {
		return fmt.Errorf("recipe %q: no command to run", name)
	}
	// Look before running: `dabs do` ALWAYS confirms — it exists to run an
	// arbitrary command in a box, so it must never launch unprompted, even with
	// no appended command. `dabs recipe`/`cast` confirm only when a caller
	// appends a command. Nothing is built or run until approved.
	if (alwaysConfirm || len(extra) > 0) && !r.confirm(confirmRecipe(name, rec, command)) {
		return fmt.Errorf("recipe %q: aborted", name)
	}

	// `dabs cast <recipe> <worktree>` binds an existing worktree to the recipe's
	// `.` source: a `worktree:`/`mount:` source attaches the worktree live (never
	// forks a new branch) and a `copy:` source snapshots it. Done before the
	// engine runs, so validate/build see plain sources.
	sources := rec.Sources
	if worktree != "" {
		sources, err = r.castSources(name, rec.Sources, worktree)
		if err != nil {
			return err
		}
	}

	drv, err := r.driverFor(rec.Target) // "" = local; a recipe may target a driver/server
	if err != nil {
		return err
	}

	// Validate every source BEFORE any side effect, so a bad recipe, a non-git
	// dir, a repo with no commits, or a missing source path all fail without
	// building an image or touching the box.
	resolved, err := r.validateSources(name, sources)
	if err != nil {
		return err
	}

	image, err := r.ensureImage(drv, name, rec.Image)
	if err != nil {
		return err
	}

	instance, kept, err := r.buildBox(drv, name, rec, image, sources, resolved)
	if err != nil {
		return err
	}
	// `cast` binds an EXISTING worktree (mounted, not cut) so buildBox never
	// journals it — record its box→worktree link here instead.
	if worktree != "" {
		if home, herr := r.data.HomeDir(); herr == nil {
			r.logWorktreeUp(instance, worktree, filepath.Join(home, ".dabs", "worktrees", worktree), name)
		}
	}
	// Delete the box once the command finishes, unless the recipe asks to keep
	// it alive so the user can run more commands in it or resume. A kept box is
	// the user's to delete with `dabs down`.
	if !rec.Keep {
		defer drv.Down(instance)
	}

	for _, k := range kept {
		fmt.Fprintf(os.Stdout, "%s worktree %s %s box\n", tui.Dot(), k, tui.Arrow())
	}
	if err := drv.Run(instance, command); err != nil {
		return fmt.Errorf("recipe %q: %w", name, err)
	}
	for _, k := range kept {
		fmt.Fprintln(os.Stdout, "\n"+tui.Success("worktree kept: %s", k))
	}
	if rec.Keep {
		fmt.Fprintf(os.Stdout, "\nbox kept: %s (dabs down %s to delete it)\n", instance, instance)
	}
	return nil
}

// resolvedSource is a source after validation: its strategy (mount/worktree/
// copy), its expanded host origin, and (for worktrees) the repo toplevel (empty
// otherwise) — the exact inputs buildBox needs to realize the source. Bundling
// the three keeps them the same length and order as the sources by construction.
type resolvedSource struct{ kind, origin, top string }

// validateSources checks every source of a recipe up front — a bad source spec,
// a non-git worktree dir, a repo with no commits, or a missing mount/copy path
// all fail HERE, before any image build or box touch. It returns one
// resolvedSource per source, in source order. Shared by runRecipe and Up so both
// validate identically.
func (r Real) validateSources(recipeName string, sources []recipe.Source) ([]resolvedSource, error) {
	resolved := make([]resolvedSource, len(sources))
	for i, s := range sources {
		kind, origin, err := s.Kind()
		if err != nil {
			return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
		}
		host, err := r.expandPath(origin)
		if err != nil {
			return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
		}
		resolved[i] = resolvedSource{kind: kind, origin: host}
		switch kind {
		case "worktree":
			top, err := r.data.GitToplevel(host)
			if err != nil {
				return nil, fmt.Errorf("recipe %q: worktree %s: %w", recipeName, s.Path, err)
			}
			if !r.data.GitHasCommits(top) {
				return nil, fmt.Errorf("recipe %q: worktree %s: repo has no commits yet — make an initial commit first", recipeName, s.Path)
			}
			resolved[i].top = top
		case "mount", "copy":
			// The host path must exist — a mount/copy of a missing path is a
			// mistake (e.g. `dabs recipe claude` before `dabs auth claude`),
			// and gives a cryptic driver failure if passed through.
			if _, err := r.data.Stat(host); errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("recipe %q: %s source %s does not exist", recipeName, kind, host)
			} else if err != nil {
				return nil, fmt.Errorf("recipe %q: %s source %s: %w", recipeName, kind, host, err)
			}
		case "perbox":
			// A per-box dir has no shared host origin to validate — it is
			// allocated fresh and empty at prep time (buildBox).
		}
	}
	return resolved, nil
}

// buildBox realizes a recipe's already-validated sources into a fresh DETACHED
// box: it cuts any worktrees, turns sources into driver mounts, brings the box
// up (image, workdir, env, target-driver), and runs the deferred in-box copies.
// It returns the instance and the worktrees it cut (kept, for the caller to
// report). No command is run and, on success, the box is left up — the caller
// owns its lifecycle (runRecipe runs the command then tears it down unless the
// recipe says keep; Up leaves it up). On any failure after the box is up it
// tears the half-built box down. Shared by runRecipe and Up so both mount
// sources identically.
func (r Real) buildBox(drv sandbox.Driver, recipeName string, rec recipe.Recipe, image string, sources []recipe.Source, resolved []resolvedSource) (instance string, kept []string, err error) {
	// Turn declared sources into driver mounts + deferred copies (creating
	// worktrees now that the image is ready).
	var mounts []sandbox.Mount
	var perbox []sandbox.Mount
	var copies []copyOp
	var cut []wtCut // fresh worktrees to journal once the box is up
	for i, s := range sources {
		rs := resolved[i]
		switch rs.kind {
		case "mount":
			mounts = append(mounts, sandbox.Mount{Host: rs.origin, Path: s.Path, RO: s.RO})
		case "worktree":
			wt, branch, err := r.addWorktree(rs.top)
			if err != nil {
				return "", nil, err
			}
			mounts = append(mounts, sandbox.Mount{Host: wt, Path: s.Path})
			kept = append(kept, fmt.Sprintf("%s (branch %s)", wt, branch))
			cut = append(cut, wtCut{name: filepath.Base(wt), path: wt})
		case "copy":
			// Mount the origin read-only at a staging path, then snapshot it into
			// the box-owned destination after up — the box gets its own copy and
			// the host is never written.
			staging := fmt.Sprintf("/.dabs/copy/%d", i)
			mounts = append(mounts, sandbox.Mount{Host: rs.origin, Path: staging, RO: true})
			copies = append(copies, copyOp{src: staging, dest: s.Path})
		case "perbox":
			// A fresh, empty, box-private host dir (under ~/.dabs/boxes/<id>/<label>)
			// mounted live at s.Path. Held aside and appended LAST so it lands on
			// top of any earlier mount it nests over (e.g. Claude's projects/ over
			// the shared vault).
			host, err := r.perboxDir(rs.origin)
			if err != nil {
				return "", nil, err
			}
			perbox = append(perbox, sandbox.Mount{Host: host, Path: s.Path})
		}
	}
	// Perbox mounts overlay earlier mounts, so they must be applied after them.
	mounts = append(mounts, perbox...)

	workdir := rec.Workdir
	if workdir == "" {
		workdir = "/work"
	}
	instance, err = drv.Up(sandbox.Spec{Name: image, Workdir: workdir, Env: rec.Env, Mounts: mounts})
	if err != nil {
		return "", nil, err
	}

	// The box exists now, so its instance can be journalled against each fresh
	// worktree it was cut for (best-effort; a log failure only warns).
	for _, c := range cut {
		r.logWorktreeUp(instance, c.name, c.path, recipeName)
	}

	for _, c := range copies {
		// argv, not a shell string — a dest with a quote/space can't break it.
		if out, err := drv.Exec(instance, []string{"mkdir", "-p", c.dest}); err != nil {
			drv.Down(instance) // don't leave a half-built box behind
			return "", nil, fmt.Errorf("recipe %q: mkdir %s: %w: %s", recipeName, c.dest, err, out)
		}
		if out, err := drv.Exec(instance, []string{"cp", "-a", c.src + "/.", c.dest}); err != nil {
			drv.Down(instance)
			return "", nil, fmt.Errorf("recipe %q: copy into %s: %w: %s", recipeName, c.dest, err, out)
		}
	}
	return instance, kept, nil
}

// resolveRecipe picks the recipe that `dabs build`/`dabs up` act on and returns
// the effective registry plus the chosen recipe name:
//   - ""     → the registry `default:` (bundled → ~/.dabs → ./dabs.yaml).
//   - a path → a dabs.yaml file (or a dir containing one) loaded as an overlay;
//     its `default:` (or its sole recipe) is used.
//   - a name → that recipe in the registry.
//
// A name that is neither a path nor a known recipe errors with the list of what
// IS known — the caller (often an agent) that guessed wrong sees the options.
func (r Real) resolveRecipe(arg string) (recipe.Registry, string, error) {
	reg, err := r.loadRegistry()
	if err != nil {
		return reg, "", err
	}
	if arg == "" {
		if reg.Default == "" {
			return reg, "", fmt.Errorf("no recipe given and no default set — choose one: %s (or set `default:` in dabs.yaml)", strings.Join(reg.Names(), ", "))
		}
		return reg, reg.Default, nil
	}
	// An arg naming an existing file or directory is a dabs.yaml to load and
	// take the default (or sole recipe) from.
	if fi, statErr := r.data.Stat(arg); statErr == nil {
		path := arg
		if fi != nil && fi.IsDir() {
			path = filepath.Join(arg, "dabs.yaml")
		}
		b, err := r.data.ReadFile(path)
		if err != nil {
			return reg, "", fmt.Errorf("recipe: %s: %w", path, err)
		}
		parsed, err := recipe.Parse(b)
		if err != nil {
			return reg, "", fmt.Errorf("recipe: %s: %w", path, err)
		}
		// A recipe loaded from an explicit path resolves its inline build paths
		// relative to the DABS.YAML's directory (as the old manifest did),
		// not the cwd — so `dabs build path/to/dir` works from anywhere.
		rebaseImagePaths(&parsed, filepath.Dir(path))
		reg.Merge(parsed)
		name := parsed.Default
		if name == "" {
			switch len(parsed.Recipes) {
			case 0:
				return reg, "", fmt.Errorf("recipe: %s defines no recipes", path)
			case 1:
				for n := range parsed.Recipes {
					name = n
				}
			default:
				return reg, "", fmt.Errorf("recipe: %s has no `default:` and %d recipes — name one: %s", path, len(parsed.Recipes), strings.Join(parsed.Names(), ", "))
			}
		}
		return reg, name, nil
	}
	// Otherwise a bare recipe name.
	if _, ok := reg.Recipes[arg]; !ok {
		return reg, "", fmt.Errorf("no recipe %q (known: %s) — build/up take a recipe name, a dabs.yaml path, or nothing (the default)", arg, strings.Join(reg.Names(), ", "))
	}
	return reg, arg, nil
}

// rebaseImagePaths anchors each recipe's inline {dockerfile,context} to dir when
// they are relative, so a dabs.yaml loaded by path builds against its OWN
// directory regardless of the cwd. Bare-name images and absolute paths are left
// alone. An empty context defaults to the dockerfile's directory, matching the
// recipe engine, but resolved up front here so ensureImage sees absolute paths.
func rebaseImagePaths(reg *recipe.Registry, dir string) {
	for n, rec := range reg.Recipes {
		if rec.Image.Dockerfile == "" {
			continue
		}
		ctx := rec.Image.Context
		if ctx == "" {
			ctx = filepath.Dir(rec.Image.Dockerfile)
		}
		if !filepath.IsAbs(rec.Image.Dockerfile) {
			rec.Image.Dockerfile = filepath.Join(dir, rec.Image.Dockerfile)
		}
		if !filepath.IsAbs(ctx) {
			ctx = filepath.Join(dir, ctx)
		}
		rec.Image.Context = ctx
		reg.Recipes[n] = rec
	}
}

// resolveBuiltImage returns the image name to BOOT for a recipe WITHOUT building
// the recipe's own Dockerfile: `dabs up` boots an image a prior `dabs build`
// produced and must not (re)build — it may run where no builder exists (a staged
// prebuilt image, a machine with no docker).
//
// A recipe with a fleet `target` (a server, docker) manages its own image
// lifecycle through the driver, and its HasImage cannot cheaply probe (the
// server driver's HasImage returns false BY DESIGN — see core/sandbox/server).
// Gating those on HasImage would wrongly reject remote `up`, so a targeted
// recipe passes its image name straight to the driver's Up (which builds/boots
// it remotely, as `dabs build` staged it). Only the LOCAL path gates: a bare
// name resolves the normal way (reuse if built, build from a bundled recipe if
// missing); an inline-{dockerfile} image that is not built yet errors, pointing
// at `dabs build`.
func (r Real) resolveBuiltImage(drv sandbox.Driver, recipeName string, img recipe.ImageRef, target string) (string, error) {
	if target != "" {
		// Remote/fleet target: don't probe locally — the driver owns build+boot.
		if img.Dockerfile != "" {
			return recipeName, nil
		}
		return img.Name, nil
	}
	if img.Dockerfile != "" {
		built, err := drv.HasImage(recipeName)
		if err != nil {
			return "", err
		}
		if !built {
			return "", fmt.Errorf("recipe %q: image not built — run `dabs build %s` first", recipeName, recipeName)
		}
		return recipeName, nil
	}
	return r.ensureImage(drv, recipeName, img)
}

// Recipes lists the known recipes and, for each, its image and what it places
// into the box — so a user (or agent) can see what a recipe does before running.
func (r Real) Recipes(p params.Recipes) error {
	// --print dumps the bundled recipes YAML — the authoring format, comments
	// and all — so `~/.dabs/recipes.yaml` can be written without guessing.
	if p.Print {
		os.Stdout.Write(recipe.Bundled)
		return nil
	}
	reg, err := r.loadRegistry()
	if err != nil {
		return err
	}
	names := reg.Names()
	if len(names) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("no recipes"))
		return nil
	}
	for _, n := range names {
		rec := reg.Recipes[n]
		img := rec.Image.Name
		if img == "" {
			img = "build:" + rec.Image.Dockerfile
		}
		head := tui.Heading(n)
		if n == reg.Default {
			head += " " + tui.Badge("default")
		}
		if rec.Description != "" {
			head += "  " + tui.Muted(rec.Description)
		}
		fmt.Fprintln(os.Stdout, head)
		fmt.Fprintln(os.Stdout, tui.Indent(tui.Muted("image=%s", img), 2))
		fmt.Fprintln(os.Stdout, tui.Indent(tui.Muted("cmd=%s", strings.Join(rec.Command, " ")), 2))
		for _, s := range rec.Sources {
			if kind, origin, err := s.Kind(); err == nil {
				fmt.Fprintf(os.Stdout, "  %s %-8s %s %s %s\n", tui.Dot(), kind, origin, tui.Arrow(), s.Path)
			}
		}
	}
	return nil
}

// confirmRecipe renders the look-before-run summary shown when a caller appends
// a command to a recipe: the recipe's box (image + what it mounts/copies) and
// the exact argv that will run in it. Deliberately plain — richer TUI later.
func confirmRecipe(name string, rec recipe.Recipe, command []string) string {
	img := rec.Image.Name
	if img == "" {
		img = "build:" + rec.Image.Dockerfile
	}
	var b strings.Builder
	fmt.Fprintf(&b, "recipe %q\n  image=%s\n", name, img)
	for _, s := range rec.Sources {
		kind, origin, err := s.Kind()
		if err != nil {
			// Show invalid sources too — the summary is the whole picture the
			// user approves; hiding a malformed source defeats look-before-run.
			fmt.Fprintf(&b, "  invalid source: %v\n", err)
			continue
		}
		fmt.Fprintf(&b, "  %-8s %s → %s\n", kind, origin, s.Path)
	}
	fmt.Fprintf(&b, "command: %s", strings.Join(command, " "))
	return b.String()
}

type copyOp struct{ src, dest string }

// wtCut is a fresh worktree buildBox cut, held until the box is up so its
// instance can be journalled against the worktree's name and absolute path.
type wtCut struct{ name, path string }

// loadRegistry builds the effective registry: bundled defaults, overlaid by the
// user's ~/.dabs/recipes.yaml, overlaid by the project's ./dabs.yaml. Later
// sources win (recipes by name, and `default`). Missing files are fine.
func (r Real) loadRegistry() (recipe.Registry, error) {
	reg, err := recipe.Parse(recipe.Bundled)
	if err != nil {
		return recipe.Registry{}, fmt.Errorf("recipe: bundled registry: %w", err)
	}
	if home, err := r.data.HomeDir(); err == nil {
		if err := r.mergeRecipeFile(&reg, filepath.Join(home, ".dabs", "recipes.yaml")); err != nil {
			return reg, err
		}
	}
	if err := r.mergeRecipeFile(&reg, "dabs.yaml"); err != nil { // project-local, cwd
		return reg, err
	}
	return reg, nil
}

// mergeRecipeFile overlays a recipes file onto reg if it exists.
func (r Real) mergeRecipeFile(reg *recipe.Registry, path string) error {
	b, err := r.data.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recipe: %s: %w", path, err)
	}
	parsed, err := recipe.Parse(b)
	if err != nil {
		return fmt.Errorf("recipe: %s: %w", path, err)
	}
	reg.Merge(parsed)
	return nil
}

// ensureImage makes the recipe's image available and returns the name to run.
// A bare name reuses an already-built image, building it from the bundled recipe
// (images/<name>) if missing. An inline {dockerfile,context} is built as the
// recipe's own name.
func (r Real) ensureImage(drv sandbox.Driver, recipeName string, img recipe.ImageRef) (string, error) {
	if img.Dockerfile != "" {
		ctx := img.Context
		if ctx == "" {
			ctx = filepath.Dir(img.Dockerfile)
		}
		dockerfile, err := filepath.Abs(img.Dockerfile)
		if err != nil {
			return "", err
		}
		ctxAbs, err := filepath.Abs(ctx)
		if err != nil {
			return "", err
		}
		if err := drv.Build(sandbox.BuildSpec{Name: recipeName, Dockerfile: dockerfile, Context: ctxAbs}); err != nil {
			return "", err
		}
		return recipeName, nil
	}
	name := img.Name
	if name == "" {
		return "", fmt.Errorf("recipe %q: image has no name and no dockerfile", recipeName)
	}
	built, err := drv.HasImage(name)
	if err != nil {
		return "", err
	}
	if built {
		return name, nil
	}
	if !r.hasBundledImage(name) {
		return "", fmt.Errorf("recipe %q: image %q is not built and dabs has no bundled recipe for it", recipeName, name)
	}
	if err := r.buildImageIfMissing(drv, name, name); err != nil {
		return "", err
	}
	return name, nil
}

func (r Real) hasBundledImage(name string) bool {
	_, err := fs.Stat(r.images, "images/"+name)
	return err == nil
}

// envRef matches $VAR and ${VAR} references in a path.
var envRef = regexp.MustCompile(`\$\{?(\w+)\}?`)

// expandPath resolves a leading ~ and any $VAR/${VAR} in a host path. An unset
// variable is an error, not a silent truncation to a shorter (wrong) path.
func (r Real) expandPath(p string) (string, error) {
	for _, m := range envRef.FindAllStringSubmatch(p, -1) {
		if _, ok := r.data.LookupEnv(m[1]); !ok {
			return "", fmt.Errorf("unset variable %s in source path %q", m[0], p)
		}
	}
	p = r.data.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := r.data.HomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p, nil
}

// addWorktree creates a fresh git worktree of top under
// ~/.dabs/worktrees/<repo>-<id> on a new branch dabs/<id> off HEAD, returning
// the worktree path and branch. It requires at least one commit (a born HEAD).
func (r Real) addWorktree(top string) (path, branch string, err error) {
	if !r.data.GitHasCommits(top) {
		return "", "", fmt.Errorf("recipe: repo has no commits yet — make an initial commit first")
	}
	home, err := r.data.HomeDir()
	if err != nil {
		return "", "", fmt.Errorf("recipe: home: %w", err)
	}
	id := randHex(4)
	base := filepath.Join(home, ".dabs", "worktrees")
	if err := r.data.MkdirAll(base, 0o755); err != nil {
		return "", "", fmt.Errorf("recipe: worktrees dir: %w", err)
	}
	path = filepath.Join(base, filepath.Base(top)+"-"+id)
	branch = "dabs/" + id
	if err := r.data.GitAddWorktree(top, branch, path); err != nil {
		return "", "", fmt.Errorf("recipe: %w", err)
	}
	return path, branch, nil
}

// perboxDir allocates a fresh, empty host dir private to one box for a `perbox:`
// source, at ~/.dabs/boxes/<id>/<label>, and creates it so the live bind mount
// resolves. The label only names the dir; the box gets a brand-new empty slice
// every up.
func (r Real) perboxDir(label string) (string, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return "", fmt.Errorf("recipe: home: %w", err)
	}
	dir := filepath.Join(home, ".dabs", "boxes", randHex(4), label)
	if err := r.data.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("recipe: per-box dir: %w", err)
	}
	return dir, nil
}

// castSources rewrites a recipe's sources to bind an existing dabs worktree
// (by name, under ~/.dabs/worktrees/<name>) to the recipe's `.` origin:
//   - worktree: . / mount: .  → mount the worktree live, PLUS mount its parent
//     .git at its own absolute path so the worktree's `.git` pointer resolves
//     and git works inside the box. `worktree:` prints a note that it attached
//     rather than forking a new branch.
//   - copy: .                 → snapshot the worktree (git stays blind in-box:
//     the object store isn't copied — that's inherent to a copy).
//
// Sources that don't name `.` (the auth vault, etc.) pass through untouched.
func (r Real) castSources(recipeName string, in []recipe.Source, worktree string) ([]recipe.Source, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return nil, err
	}
	wt := filepath.Join(home, ".dabs", "worktrees", worktree)
	if _, err := r.data.Stat(wt); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("cast: no worktree %q at %s (see: dabs worktrees ls)", worktree, wt)
	} else if err != nil {
		return nil, fmt.Errorf("cast: worktree %s: %w", wt, err)
	}
	gitDir, err := r.data.GitCommonDir(wt)
	if err != nil {
		return nil, fmt.Errorf("cast: %s is not a git worktree: %w", wt, err)
	}

	out := make([]recipe.Source, 0, len(in)+1)
	var gitMounted, bound bool
	for _, s := range in {
		kind, origin, err := s.Kind()
		if err != nil {
			return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
		}
		if origin != "." {
			out = append(out, s)
			continue
		}
		bound = true
		switch kind {
		case "worktree", "mount":
			if kind == "worktree" {
				fmt.Fprintln(os.Stdout, tui.Muted("cast: recipe wants a fresh worktree; casting onto %s — mounting it instead.", wt))
			}
			out = append(out, recipe.Source{Mount: wt, Path: s.Path, RO: s.RO})
			if !gitMounted { // the shared object store, once, so git resolves in-box
				out = append(out, recipe.Source{Mount: gitDir, Path: gitDir})
				gitMounted = true
			}
		case "copy":
			out = append(out, recipe.Source{Copy: wt, Path: s.Path})
		}
	}
	if !bound {
		return nil, fmt.Errorf("cast: recipe %q has no `.` source to bind the worktree to", recipeName)
	}
	return out, nil
}

// randHex returns 2n hex chars of cryptographic randomness for naming.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
