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
	rec, err := reg.Get(p.Name)
	if err != nil {
		return err
	}
	if len(rec.Command) == 0 {
		return fmt.Errorf("recipe %q: no command to run", p.Name)
	}
	drv, err := r.driverFor("") // recipes are a local concern
	if err != nil {
		return err
	}

	// Validate every source BEFORE any side effect, so a bad recipe, a non-git
	// dir, a repo with no commits, or a missing source path all fail without
	// building an image or touching the box.
	kinds := make([]string, len(rec.Sources))
	origins := make([]string, len(rec.Sources))
	tops := make([]string, len(rec.Sources))
	for i, s := range rec.Sources {
		kind, origin, err := s.Kind()
		if err != nil {
			return fmt.Errorf("recipe %q: %w", p.Name, err)
		}
		host, err := r.expandPath(origin)
		if err != nil {
			return fmt.Errorf("recipe %q: %w", p.Name, err)
		}
		kinds[i], origins[i] = kind, host
		switch kind {
		case "worktree":
			top, err := r.data.GitToplevel(host)
			if err != nil {
				return fmt.Errorf("recipe %q: worktree %s: %w", p.Name, s.Path, err)
			}
			if !r.data.GitHasCommits(top) {
				return fmt.Errorf("recipe %q: worktree %s: repo has no commits yet — make an initial commit first", p.Name, s.Path)
			}
			tops[i] = top
		case "mount", "copy":
			// The host path must exist — a mount/copy of a missing path is a
			// mistake (e.g. `dabs recipe claude` before `dabs auth claude`),
			// and gives a cryptic driver failure if passed through.
			if _, err := r.data.Stat(host); errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("recipe %q: %s source %s does not exist", p.Name, kind, host)
			} else if err != nil {
				return fmt.Errorf("recipe %q: %s source %s: %w", p.Name, kind, host, err)
			}
		}
	}

	image, err := r.ensureImage(drv, p.Name, rec.Image)
	if err != nil {
		return err
	}

	// Turn declared sources into driver mounts + deferred copies (creating
	// worktrees now that the image is ready).
	var mounts []sandbox.Mount
	var copies []copyOp
	var kept []string
	for i, s := range rec.Sources {
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
		script := fmt.Sprintf("mkdir -p '%s' && cp -a '%s/.' '%s/'", c.dest, c.src, c.dest)
		if out, err := drv.Exec(instance, []string{"sh", "-c", script}); err != nil {
			return fmt.Errorf("recipe %q: copy into %s: %w: %s", p.Name, c.dest, err, out)
		}
	}

	for _, k := range kept {
		fmt.Fprintf(os.Stdout, "worktree %s → box\n", k)
	}
	if err := drv.Run(instance, rec.Command); err != nil {
		return fmt.Errorf("recipe %q: %w", p.Name, err)
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
		fmt.Fprintf(os.Stdout, "%-14s image=%s cmd=%s\n", n, img, strings.Join(rec.Command, " "))
		for _, s := range rec.Sources {
			if kind, origin, err := s.Kind(); err == nil {
				fmt.Fprintf(os.Stdout, "  %-8s %s → %s\n", kind, origin, s.Path)
			}
		}
	}
	return nil
}

type copyOp struct{ src, dest string }

// loadRegistry parses the bundled recipes and overlays the user's
// ~/.dabs/recipes.yaml (missing file is fine). All IO goes through the data seam.
func (r Real) loadRegistry() (recipe.Registry, error) {
	reg, err := recipe.Parse(recipe.Bundled)
	if err != nil {
		return recipe.Registry{}, fmt.Errorf("recipe: bundled registry: %w", err)
	}
	home, err := r.data.HomeDir()
	if err != nil {
		return reg, nil
	}
	userPath := filepath.Join(home, ".dabs", "recipes.yaml")
	b, err := r.data.ReadFile(userPath)
	if errors.Is(err, fs.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return reg, fmt.Errorf("recipe: %s: %w", userPath, err)
	}
	user, err := recipe.Parse(b)
	if err != nil {
		return reg, fmt.Errorf("recipe: %s: %w", userPath, err)
	}
	reg.Merge(user)
	return reg, nil
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
		if r.data.Getenv(m[1]) == "" {
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

// randHex returns 2n hex chars of cryptographic randomness for naming.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
