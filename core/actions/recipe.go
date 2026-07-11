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
)

// Recipe runs the named recipe: it ensures the image exists, prepares the
// recipe's sources (live mounts, fresh git worktrees, and up-time copies),
// brings up a box with them, runs the recipe's command interactively, and tears
// the box down on exit. Worktrees are KEPT (paths printed) so no in-box work is
// silently discarded. Everything the box does is declared in the recipe — this
// is the generic engine `dabs recipe claude` (and any user recipe) runs on.
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
	rec, err := reg.Get(name)
	if err != nil {
		return err
	}
	if len(rec.Command) == 0 {
		return fmt.Errorf("recipe %q: no command to run", name)
	}

	// `dabs cast <recipe> <worktree>` binds an existing worktree to the recipe's
	// `.` source: a `worktree:`/`mount:` source attaches the worktree live (never
	// forks a new branch) and a `copy:` source snapshots it. Done before the
	// engine runs, so validate/build see plain sources.
	sources := rec.Sources
	if p.Worktree != "" {
		sources, err = r.castSources(name, rec.Sources, p.Worktree)
		if err != nil {
			return err
		}
	}

	drv, err := r.driverFor("") // recipes are a local concern
	if err != nil {
		return err
	}

	// Validate every source BEFORE any side effect, so a bad recipe, a non-git
	// dir, a repo with no commits, or a missing source path all fail without
	// building an image or touching the box.
	kinds := make([]string, len(sources))
	origins := make([]string, len(sources))
	tops := make([]string, len(sources))
	for i, s := range sources {
		kind, origin, err := s.Kind()
		if err != nil {
			return fmt.Errorf("recipe %q: %w", name, err)
		}
		host, err := r.expandPath(origin)
		if err != nil {
			return fmt.Errorf("recipe %q: %w", name, err)
		}
		kinds[i], origins[i] = kind, host
		switch kind {
		case "worktree":
			top, err := r.data.GitToplevel(host)
			if err != nil {
				return fmt.Errorf("recipe %q: worktree %s: %w", name, s.Path, err)
			}
			if !r.data.GitHasCommits(top) {
				return fmt.Errorf("recipe %q: worktree %s: repo has no commits yet — make an initial commit first", name, s.Path)
			}
			tops[i] = top
		case "mount", "copy":
			// The host path must exist — a mount/copy of a missing path is a
			// mistake (e.g. `dabs recipe claude` before `dabs auth claude`),
			// and gives a cryptic driver failure if passed through.
			if _, err := r.data.Stat(host); errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("recipe %q: %s source %s does not exist", name, kind, host)
			} else if err != nil {
				return fmt.Errorf("recipe %q: %s source %s: %w", name, kind, host, err)
			}
		}
	}

	image, err := r.ensureImage(drv, name, rec.Image)
	if err != nil {
		return err
	}

	// Turn declared sources into driver mounts + deferred copies (creating
	// worktrees now that the image is ready).
	var mounts []sandbox.Mount
	var copies []copyOp
	var kept []string
	for i, s := range sources {
		switch kinds[i] {
		case "mount":
			mounts = append(mounts, sandbox.Mount{Host: origins[i], Path: s.Path, RO: s.RO})
		case "worktree":
			wt, branch, err := r.addWorktree(tops[i])
			if err != nil {
				return err
			}
			mounts = append(mounts, sandbox.Mount{Host: wt, Path: s.Path})
			kept = append(kept, fmt.Sprintf("%s (branch %s)", wt, branch))
		case "copy":
			// Mount the origin read-only at a staging path, then snapshot it into
			// the box-owned destination after up — the box gets its own copy and
			// the host is never written.
			staging := fmt.Sprintf("/.dabs/copy/%d", i)
			mounts = append(mounts, sandbox.Mount{Host: origins[i], Path: staging, RO: true})
			copies = append(copies, copyOp{src: staging, dest: s.Path})
		}
	}

	workdir := rec.Workdir
	if workdir == "" {
		workdir = "/work"
	}
	instance, err := drv.Up(sandbox.Spec{Name: image, Workdir: workdir, Env: rec.Env, Mounts: mounts})
	if err != nil {
		return err
	}
	defer drv.Down(instance)

	for _, c := range copies {
		// argv, not a shell string — a dest with a quote/space can't break it.
		if out, err := drv.Exec(instance, []string{"mkdir", "-p", c.dest}); err != nil {
			return fmt.Errorf("recipe %q: mkdir %s: %w: %s", name, c.dest, err, out)
		}
		if out, err := drv.Exec(instance, []string{"cp", "-a", c.src + "/.", c.dest}); err != nil {
			return fmt.Errorf("recipe %q: copy into %s: %w: %s", name, c.dest, err, out)
		}
	}

	for _, k := range kept {
		fmt.Fprintf(os.Stdout, "worktree %s → box\n", k)
	}
	if err := drv.Run(instance, rec.Command); err != nil {
		return fmt.Errorf("recipe %q: %w", name, err)
	}
	for _, k := range kept {
		fmt.Fprintf(os.Stdout, "\nworktree kept: %s\n", k)
	}
	return nil
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
		fmt.Fprintln(os.Stdout, "no recipes")
		return nil
	}
	for _, n := range names {
		rec := reg.Recipes[n]
		img := rec.Image.Name
		if img == "" {
			img = "build:" + rec.Image.Dockerfile
		}
		mark := ""
		if n == reg.Default {
			mark = " (default)"
		}
		fmt.Fprintf(os.Stdout, "%-14s image=%s cmd=%s%s\n", n, img, strings.Join(rec.Command, " "), mark)
		for _, s := range rec.Sources {
			if kind, origin, err := s.Kind(); err == nil {
				fmt.Fprintf(os.Stdout, "  %-8s %s → %s\n", kind, origin, s.Path)
			}
		}
	}
	return nil
}

type copyOp struct{ src, dest string }

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
				fmt.Fprintf(os.Stdout, "cast: recipe wants a fresh worktree; casting onto %s — mounting it instead.\n", wt)
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
