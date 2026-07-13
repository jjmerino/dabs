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
	"sort"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// Recipe runs `dabs recipe [name] [cmd…]`. Three shapes:
//
//   - No args, or a leading `--` (params.Default): the registry DEFAULT recipe
//     runs (the dabs.yaml `default:`, else the bundled `sh` box). A `--` appends
//     everything after it to that recipe's command; no args runs its own command.
//     This path ALWAYS confirms — it runs an arbitrary command in a box, so it
//     must never launch unprompted (the replacement for the old `dabs do`).
//   - A first positional naming a KNOWN recipe: that recipe, the rest appended to
//     its command, confirmed only when a command is appended.
//   - A first positional that is neither `--` nor a known recipe: an ERROR listing
//     the known recipes — a bare typo must not silently become a command. The hint
//     points at `-- <cmd>` for running a command on the default recipe.
//
// `--worktree <wt>` (p.Worktree) binds the recipe's `.` source to an EXISTING
// dabs worktree instead of the cwd, mounting its parent .git so git works in-box.
//
// `--detach` (p.Detach) is a fourth shape: it boots a NEW pristine DETACHED box
// from the resolved recipe (Args[0] as a name or dabs.yaml path, else the
// default) and does NOT run the recipe's command.
func (r Real) Recipe(p params.Recipe) error {
	if p.Detach {
		arg := ""
		if len(p.Args) > 0 {
			arg = p.Args[0]
		}
		return r.upDetached(arg, p.Worktree)
	}
	reg, err := r.loadRegistry()
	if err != nil {
		return err
	}
	if p.Name != "" {
		return r.runRecipe(reg, p.Name, p.Worktree, nil, false)
	}
	if p.Default || len(p.Args) == 0 {
		name := reg.Default
		if name == "" {
			name = "sh" // no project default → the bundled generic shell box
		}
		return r.runRecipe(reg, name, "", p.Args, true)
	}
	name := p.Args[0]
	if _, ok := reg.Recipes[name]; !ok {
		return fmt.Errorf("no recipe %q (known: %s) — or `dabs recipe -- %s` to run it as a command on the default recipe", name, strings.Join(reg.Names(), ", "), name)
	}
	return r.runRecipe(reg, name, p.Worktree, p.Args[1:], false)
}

// runRecipe is the shared engine behind `dabs recipe` (with or without
// `--worktree`): it ensures the image exists, prepares the recipe's sources (live
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
	boxless := rec.Image.Name == "" && rec.Image.Dockerfile == ""
	if err := r.checkSources(name, rec.Sources, boxless); err != nil {
		return err
	}
	command := append(append([]string{}, rec.Command...), extra...)
	// A recipe with no image is a recipe for a PLACE, not a box: it provisions its
	// nodes (a worktree, a directory) and stops. Nodes do not need a box; a box
	// mounts what a node owns.
	if boxless {
		return r.provisionNodes(name, rec, worktree)
	}
	// An empty recipe command has nothing to run, and appended argv cannot supply
	// one: it is appended to the recipe's command, so with no command it would
	// reach the driver as bare options (bwrap: Unknown option). The recipe's own
	// command is what defines the run.
	if len(rec.Command) == 0 {
		return fmt.Errorf("recipe %q: no command to run", name)
	}
	// A worktree argument resolves by unambiguous prefix, git-style, like every
	// other name dabs takes — done before bindWorktree rewrites the sources.
	if worktree != "" {
		full, werr := r.resolveWorktreeArg(worktree)
		if werr != nil {
			return werr
		}
		worktree = full
	}
	// Look before running: the default-recipe path ALWAYS confirms — it exists to
	// run an arbitrary command in a box, so it must never launch unprompted, even
	// with no appended command. A named recipe confirms only when a
	// caller appends a command. Nothing is built or run until approved.
	if (alwaysConfirm || len(extra) > 0) && !r.confirm(confirmRecipe(name, rec, command)) {
		return fmt.Errorf("recipe %q: aborted", name)
	}

	// `--worktree <wt>` binds an existing worktree to the recipe's `.` source: a
	// `worktree:`/`mount:` source attaches the worktree live (never forks a new
	// branch) and a `copy:` source snapshots it. Done before the engine runs, so
	// validate/build see plain sources.
	sources := rec.Sources
	if worktree != "" {
		sources, err = r.bindWorktree(name, rec.Sources, worktree)
		if err != nil {
			return err
		}
	}

	drv, err := r.driverFor(rec.Target) // "" = local; a recipe may target a driver/server
	if err != nil {
		return err
	}

	// Cut the PLACE first: a box names its parent's spaces ($PARENT_VOLUME), and a
	// parent must exist to be named.
	_, tip, hosts, kept, cut, err := r.provisionPlaces(name, sources, worktree)
	if err != nil {
		return err
	}
	boxID, vars, err := r.mintBoxNode(name, tip)
	if err != nil {
		return err
	}
	resolved, err := r.validateSources(name, sources, vars, hosts)
	if err != nil {
		return err
	}

	image, err := r.ensureImage(drv, name, rec.Image)
	if err != nil {
		return err
	}

	instance, err := r.buildBox(drv, name, boxID, tip, rec, image, sources, resolved, cut)
	if err != nil {
		return err
	}
	// `--worktree` binds an EXISTING worktree (mounted, not cut) so buildBox never
	// journals it — record its box→worktree link here instead.
	if worktree != "" {
		if data, derr := r.resolveNodeData(worktree); derr == nil {
			r.logWorktreeUp(instance, worktree, data, name)
		}
	}
	// Delete the box once the command finishes, unless the recipe asks to keep
	// it alive so the user can run more commands in it or resume. A kept box is
	// the user's to delete with `dabs rm`.
	if !rec.Keep {
		// teardown (not a bare drv.Down) so a journaled worktree-backed box also
		// gets its matching `down` — otherwise a non-keep `worktree:` recipe would
		// leave a dead box reading as live forever.
		defer r.teardown(drv, instance)
	}

	for _, k := range kept {
		fmt.Fprintf(os.Stdout, "%s %s %s box\n", tui.Dot(), k, tui.Arrow())
	}
	// Say what is about to run. A command that prints nothing until it finishes
	// (an agent thinking, a long build) is indistinguishable from a hang without
	// this line.
	fmt.Fprintf(os.Stdout, "%s %s\n\n", tui.Muted("running:"), strings.Join(command, " "))
	if err := drv.Run(instance, command); err != nil {
		return fmt.Errorf("recipe %q: %w", name, err)
	}
	for _, k := range kept {
		fmt.Fprintln(os.Stdout, "\n"+tui.Success("kept: %s", k))
	}
	if rec.Keep {
		fmt.Fprintf(os.Stdout, "\nbox kept: %s (dabs rm %s to delete it)\n", instance, instance)
	}
	return nil
}

// sortMountsByDepth orders mounts parent-before-child by box path, so a mount
// NESTED inside another lands on top of it instead of under it.
//
// Drivers do not agree on this. bwrap binds in argv order, so a parent listed
// after its child masks the child — silently, with the parent's own content at
// the child's path. Apple's `container` and docker resolve nesting themselves and
// do not care. A recipe authored on macOS would therefore break on Linux, so the
// order is decided HERE and every driver gets the same one.
//
// Stable: mounts at the same depth keep their declared order.
func sortMountsByDepth(mounts []sandbox.Mount) {
	sort.SliceStable(mounts, func(i, j int) bool {
		return pathDepth(mounts[i].Path) < pathDepth(mounts[j].Path)
	})
}

// pathDepth counts the segments in a box path: /work → 1, /root/.claude → 2.
func pathDepth(p string) int {
	return len(strings.Split(strings.Trim(filepath.Clean(p), "/"), "/"))
}

// provisionNodes runs a recipe that has no image: it marks the project, cuts
// whatever places the recipe declares, and prints them. There is no box, so
// nothing is mounted and nothing is torn down — the places persist, and a later
// recipe (or `dabs recipe --worktree`) can put a box on one.
func (r Real) provisionNodes(name string, rec recipe.Recipe, worktree string) error {
	if worktree != "" {
		return fmt.Errorf("recipe %q: has no image, so there is no box to put onto a worktree", name)
	}
	project, err := r.ensureProjectNode(name)
	if err != nil {
		return err
	}
	made := 0
	for _, s := range rec.Sources {
		kind, origin, err := s.Kind()
		if err != nil {
			return fmt.Errorf("recipe %q: %w", name, err)
		}
		host, err := r.expandPath(origin)
		if err != nil {
			return fmt.Errorf("recipe %q: %w", name, err)
		}
		if host, err = r.absPath(host); err != nil {
			return fmt.Errorf("recipe %q: %w", name, err)
		}
		switch kind {
		case "worktree":
			top, err := r.data.GitToplevel(host)
			if err != nil {
				return fmt.Errorf("recipe %q: worktree %s: %w", name, origin, err)
			}
			wt, branch, id, err := r.addWorktree(top, name, project)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "%s %s %s %s\n", tui.Success("worktree"), tui.Accent(id), tui.Muted("branch "+branch+" ·"), tui.Muted(wt))
			made++
		case "copy":
			id, _ := mintNodeID(filepath.Base(host))
			dir, err := r.workdirData(id)
			if err != nil {
				return err
			}
			if err := r.data.CopyDir(host, dir); err != nil {
				return fmt.Errorf("recipe %q: copy %s: %w", name, host, err)
			}
			// Dir is the copy's own location — where it lives, not its source.
			if err := r.writeNode(Node{ID: id, Kind: KindWorkdir, Parent: project, Recipe: name, Created: stampNow(), Dir: dir}); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "%s %s %s\n", tui.Success("workdir"), tui.Accent(id), tui.Muted(dir))
			made++
		case "mount":
			// A live mount makes no place: without a box there is nothing to mount
			// it into, and the project already marks the directory.
		}
	}
	if made == 0 {
		return fmt.Errorf("recipe %q: has no image and no source that makes a place — it would do nothing", name)
	}
	return nil
}

// workdirData is the directory a workdir node owns: its own copy of the code,
// in the node's ephemeral space, so `down` asks before reaping it and you can
// read it on the host.
func (r Real) workdirData(id string) (string, error) {
	eph, err := r.resolveNodeSpace(id, SpaceEphemeral)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(eph, "work")
	return dir, r.data.MkdirAll(dir, 0o755)
}

// mintBoxNode names the box's node before the box exists, and returns the three
// space paths a recipe may name as $NODE_VOLUME / $NODE_EPHEMERAL / $NODE_TMP.
// The id is minted first because a source may mount a space, and a mount needs a
// path before the driver is called.
func (r Real) mintBoxNode(recipeName, parent string) (id string, vars map[string]string, err error) {
	id, _ = mintNodeID(recipeName)
	vars, err = r.spaceVars(id, "NODE")
	if err != nil {
		return "", nil, err
	}
	// $PARENT_* is the PLACE this box runs in — its worktree, its directory, or
	// the project. A place is re-entered by every later box, so what a box wants
	// back next time (an agent's sessions) belongs there, not in its own spaces:
	// a box node is minted fresh every run and never returns.
	pv, err := r.spaceVars(parent, "PARENT")
	if err != nil {
		return "", nil, err
	}
	for k, v := range pv {
		vars[k] = v
	}
	return id, vars, nil
}

// spaceVars names a node's three spaces under a prefix, for source paths.
func (r Real) spaceVars(id, prefix string) (map[string]string, error) {
	vars := map[string]string{}
	for v, space := range map[string]string{
		"_VOLUME":    SpaceVolume,
		"_EPHEMERAL": SpaceEphemeral,
		"_TMP":       SpaceTmp,
	} {
		p, err := r.resolveNodeSpace(id, space)
		if err != nil {
			return nil, err
		}
		vars[prefix+v] = p
	}
	return vars, nil
}

// provisionPlaces cuts whatever PLACE a recipe declares — a worktree, a directory
// holding its own copy — and returns the chain tip a box will stack on, plus the
// host path each `.` source resolved to.
//
// It runs BEFORE the box node is named, because a box names its parent's spaces
// ($PARENT_VOLUME) and a parent must exist to be named.
//
// `at:` says where a provisioned place puts its bytes on the host, in the new
// node's own spaces — so the recipe, not this function, decides what `down` may
// reap.
func (r Real) provisionPlaces(recipeName string, sources []recipe.Source, boundWorktree string) (project, tip string, hosts map[int]string, kept []string, cut []wtCut, err error) {
	project, err = r.ensureProjectNode(recipeName)
	if err != nil {
		return "", "", nil, nil, nil, err
	}
	tip, hosts = project, map[int]string{}
	if boundWorktree != "" {
		// --worktree binds an EXISTING place; bindWorktree already rewrote the `.`
		// source to mount it, so there is nothing to provision and the tip is that node.
		return project, boundWorktree, hosts, nil, nil, nil
	}
	for i, s := range sources {
		kind, origin, kerr := s.Kind()
		if kerr != nil {
			return "", "", nil, nil, nil, fmt.Errorf("recipe %q: %w", recipeName, kerr)
		}
		if !isDotSource(s) {
			continue
		}
		host, herr := r.expandPath(origin)
		if herr != nil {
			return "", "", nil, nil, nil, fmt.Errorf("recipe %q: %w", recipeName, herr)
		}
		if host, herr = r.absPath(host); herr != nil {
			return "", "", nil, nil, nil, fmt.Errorf("recipe %q: %w", recipeName, herr)
		}
		switch kind {
		case "worktree":
			top, terr := r.data.GitToplevel(host)
			if terr != nil {
				return "", "", nil, nil, nil, fmt.Errorf("recipe %q: worktree %s: %w", recipeName, origin, terr)
			}
			id, short := mintNodeID(filepath.Base(top))
			at, aerr := r.placeAt(s, id, "worktree")
			if aerr != nil {
				return "", "", nil, nil, nil, fmt.Errorf("recipe %q: %w", recipeName, aerr)
			}
			branch := "dabs/" + short
			if err := r.cutWorktree(top, branch, at, id, recipeName, project); err != nil {
				return "", "", nil, nil, nil, err
			}
			tip, hosts[i] = id, at
			kept = append(kept, fmt.Sprintf("worktree %s (branch %s)", at, branch))
			cut = append(cut, wtCut{name: id, path: at})
		case "copy":
			// A copy makes a directory, so every run makes ANOTHER one — the way
			// every worktree run cuts another branch. That is what lets two runs
			// over one directory be worked in parallel.
			id, _ := mintNodeID(filepath.Base(host))
			at, aerr := r.placeAt(s, id, "work")
			if aerr != nil {
				return "", "", nil, nil, nil, fmt.Errorf("recipe %q: %w", recipeName, aerr)
			}
			// A copy whose destination is inside its own source makes cp recurse
			// into itself (dest/dest/dest…) until the path is too long — reject it
			// before a single byte is copied.
			if pathInside(at, host) {
				return "", "", nil, nil, nil, fmt.Errorf("recipe %q: copy destination %s is inside the copy source %s — it would recurse into itself", recipeName, at, host)
			}
			if err := r.data.MkdirAll(at, 0o755); err != nil {
				return "", "", nil, nil, nil, err
			}
			if err := r.data.CopyDir(host, at); err != nil {
				return "", "", nil, nil, nil, fmt.Errorf("recipe %q: copy %s: %w", recipeName, host, err)
			}
			// A workdir's own directory is where the copy LIVES, not where it was
			// copied from — that is what `ls` shows and where a user looks. The
			// source is the parent project's directory.
			if err := r.writeNode(Node{ID: id, Kind: KindWorkdir, Parent: project, Recipe: recipeName, Created: stampNow(), Dir: at}); err != nil {
				return "", "", nil, nil, nil, err
			}
			tip, hosts[i] = id, at
			kept = append(kept, "workdir "+at)
		case "mount":
			// A live mount provisions no middle node: the box stands directly on
			// the project (the diamond's direct edge). The place IS the live host
			// directory, so there is nothing to make.
			tip, hosts[i] = project, host
		}
	}
	return project, tip, hosts, kept, cut, nil
}

// placeAt resolves a provisioning source's `at:` — where it puts its bytes in the
// NEW node's spaces. Unset, it is that node's ephemeral space: dabs made it, so
// dabs may reap it, but `down` asks first because that is where work lives.
func (r Real) placeAt(s recipe.Source, id, leaf string) (string, error) {
	vars, err := r.spaceVars(id, "NODE")
	if err != nil {
		return "", err
	}
	if s.At == "" {
		return filepath.Join(vars["NODE_EPHEMERAL"], leaf), nil
	}
	return r.expandPathWith(s.At, vars)
}

// cutWorktree checks out a new branch off HEAD into at, and records the node.
func (r Real) cutWorktree(top, branch, at, id, recipeName, parent string) error {
	if !r.data.GitHasCommits(top) {
		return fmt.Errorf("recipe: repo has no commits yet — make an initial commit first")
	}
	if err := r.data.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return fmt.Errorf("recipe: node dir: %w", err)
	}
	if err := r.data.GitAddWorktree(top, branch, at); err != nil {
		return fmt.Errorf("recipe: %w", err)
	}
	return r.writeNode(Node{
		ID:       id,
		Kind:     KindWorktree,
		Parent:   parent,
		Recipe:   recipeName,
		Created:  stampNow(),
		Dir:      at,
		Worktree: &NodeWorktree{Branch: branch, Repo: top},
	})
}

// resolvedSource is a source after validation: its strategy (mount/worktree/
// copy), its expanded host origin, and (for worktrees) the repo toplevel (empty
// otherwise) — the exact inputs buildBox needs to realize the source. Bundling
// the three keeps them the same length and order as the sources by construction.
type resolvedSource struct{ kind, origin, top string }

// validateSources checks every source of a recipe up front — a bad source spec,
// a non-git worktree dir, a repo with no commits, or a missing mount/copy path
// all fail HERE, before any image build or box touch. It returns one
// resolvedSource per source, in source order. Shared by runRecipe and the
// detach path so both validate identically.
func (r Real) validateSources(recipeName string, sources []recipe.Source, vars map[string]string, hosts map[int]string) ([]resolvedSource, error) {
	resolved := make([]resolvedSource, len(sources))
	for i, s := range sources {
		kind, origin, err := s.Kind()
		if err != nil {
			return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
		}
		if s.NeedsBoxPath() {
			return nil, fmt.Errorf("recipe %q: source %s:%s has no path — say where it lands in the box", recipeName, kind, origin)
		}
		// A `.` source was already provisioned into a place; its host is that place.
		if h, ok := hosts[i]; ok {
			resolved[i] = resolvedSource{kind: kind, origin: h}
			continue
		}
		host, err := r.expandPathWith(origin, vars)
		if err != nil {
			return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
		}
		// Drivers take exact, absolute host paths — a relative mount/copy origin
		// (`.`, or `./stage`) is resolved HERE, against the cwd, so every driver
		// gets the same path. Passing it through verbatim happens to work on some
		// drivers and dies on others (docker: "mount path must be absolute").
		// `perbox:` has no host origin (it is a label) and a `worktree:` origin
		// never reaches a driver (git resolves it to an absolute toplevel below).
		if kind == "mount" || kind == "copy" {
			abs, err := r.absPath(host)
			if err != nil {
				return nil, fmt.Errorf("recipe %q: source %s: %w", recipeName, host, err)
			}
			host = abs
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
			// The host path must exist. A mount or copy of a missing path is a
			// typo, and passing it through gives a cryptic driver failure. A
			// source that MEANS to create its origin says mkmount.
			if _, err := r.data.Stat(host); errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("recipe %q: %s source %s does not exist (use mkmount: to create it)", recipeName, kind, host)
			} else if err != nil {
				return nil, fmt.Errorf("recipe %q: %s source %s: %w", recipeName, kind, host, err)
			}
		case "mkmount":
			// Created at prep time (buildBox), so a missing origin is the point.
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
// recipe says keep; `--detach` leaves it up). On any failure after the box is up
// it tears the half-built box down. Shared by runRecipe and the detach path so
// both mount sources identically.
func (r Real) buildBox(drv sandbox.Driver, recipeName, boxID, tip string, rec recipe.Recipe, image string, sources []recipe.Source, resolved []resolvedSource, cut []wtCut) (instance string, err error) {
	// Places are already cut (provisionPlaces): a `.` source's origin is the
	// directory that place owns. What is left is turning every source into a mount.
	var mounts []sandbox.Mount
	for i, s := range sources {
		rs := resolved[i]
		switch rs.kind {
		case "mount", "worktree", "copy":
			// worktree and copy own a directory on the host; a `.` mount IS the host
			// directory. All three are one thing to a driver: a live bind.
			mounts = append(mounts, sandbox.Mount{Host: rs.origin, Path: s.Path, RO: s.RO})
		case "mkmount":
			// The origin is the recipe's to name and dabs's to create — 0700,
			// because this is where a harness puts a credential.
			if err := r.data.MkdirAll(rs.origin, 0o700); err != nil {
				return "", fmt.Errorf("recipe %q: mkmount %s: %w", recipeName, rs.origin, err)
			}
			mounts = append(mounts, sandbox.Mount{Host: rs.origin, Path: s.Path, RO: s.RO})
		}
	}
	sortMountsByDepth(mounts)

	workdir := rec.Workdir
	if workdir == "" {
		workdir = "/work"
	}
	// A recipe's env is passed to the driver as-is, and setting PATH there REPLACES
	// the image PATH rather than extending it — so even the recipe's own command
	// may stop resolving. Warn (stderr, never stdout) so the box still comes up.
	if _, ok := rec.Env["PATH"]; ok {
		fmt.Fprintln(os.Stderr, tui.Warn("recipe %q sets PATH in env, which REPLACES the image PATH — commands in the box may not resolve", recipeName))
	}
	instance, err = drv.Up(sandbox.Spec{Name: image, Workdir: workdir, Env: rec.Env, Mounts: mounts})
	if err != nil {
		return "", err
	}

	// Mark the box: the node was named before the box came up (its spaces had to
	// exist to be mounted), so record which sandbox it turned out to be.
	if err := r.writeNode(Node{
		ID:       boxID,
		Kind:     KindBox,
		Parent:   tip,
		Recipe:   recipeName,
		Created:  stampNow(),
		Instance: instance,
	}); err != nil {
		return "", err
	}

	// The box exists now, so its instance can be journalled against each fresh
	// worktree it was cut for (best-effort; a log failure only warns).
	for _, c := range cut {
		r.logWorktreeUp(instance, c.name, c.path, recipeName)
	}

	return instance, nil
}

// resolveRecipe picks the recipe that `dabs build`/`dabs recipe --detach` act on
// and returns the effective registry plus the chosen recipe name:
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
		// relative to the DABS.YAML's directory,
		// not the cwd — so `dabs build path/to/dir` works from anywhere.
		rebaseImagePaths(&parsed, filepath.Dir(path))
		rebaseSourcePaths(&parsed, filepath.Dir(path))
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
		return reg, "", fmt.Errorf("no recipe %q (known: %s) — build/detach take a recipe name, a dabs.yaml path, or nothing (the default)", arg, strings.Join(reg.Names(), ", "))
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

// rebaseSourcePaths anchors each recipe's RELATIVE source origins (mount/copy/
// worktree) to dir, for a dabs.yaml loaded BY PATH — the same rule
// rebaseImagePaths applies to its image, so `dabs recipe path/to/box --detach` provisions the
// same box from any cwd. Absolute origins, `~`/`$VAR` origins (expanded later),
// and `perbox:` labels are left alone.
//
// Registry recipes (bundled, ~/.dabs/recipes.yaml, ./dabs.yaml) are NOT rebased:
// their relative origins stay cwd-relative, which is what `mount: .` = "your
// cwd, live" means. For a project ./dabs.yaml the two are the same directory.
func rebaseSourcePaths(reg *recipe.Registry, dir string) {
	rebase := func(p string) string {
		if !isHostRelative(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	for n, rec := range reg.Recipes {
		srcs := make([]recipe.Source, len(rec.Sources))
		copy(srcs, rec.Sources)
		for i := range srcs {
			srcs[i].Mount = rebase(srcs[i].Mount)
			srcs[i].Copy = rebase(srcs[i].Copy)
			srcs[i].Worktree = rebase(srcs[i].Worktree)
		}
		rec.Sources = srcs
		reg.Recipes[n] = rec
	}
}

// resolveBuiltImage returns the image name to BOOT for a recipe WITHOUT building
// the recipe's own Dockerfile: `dabs recipe --detach` boots an image a prior
// `dabs build` produced and must not (re)build — it may run where no builder
// exists (a staged prebuilt image, a machine with no docker).
//
// A recipe with a fleet `target` (a server, docker) manages its own image
// lifecycle through the driver, and its HasImage cannot cheaply probe (the
// server driver's HasImage returns false BY DESIGN — see core/sandbox/server).
// Gating those on HasImage would wrongly reject a remote detach, so a targeted
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
// (images/<name>) if missing. An inline {dockerfile,context} reuses an
// already-built image too, and is built as the recipe's own name only when
// missing — an edited Dockerfile is rebuilt by the explicit `dabs build` verb.
func (r Real) ensureImage(drv sandbox.Driver, recipeName string, img recipe.ImageRef) (string, error) {
	if img.Dockerfile != "" {
		built, err := drv.HasImage(recipeName)
		if err != nil {
			return "", err
		}
		if built {
			return recipeName, nil
		}
		return r.buildDockerImage(drv, recipeName, img)
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

// buildDockerImage builds a recipe's inline {dockerfile,context} image as the
// recipe's own name, unconditionally. `dabs build` calls it to force a rebuild
// even when the image already exists; ensureImage calls it only when the image
// is missing.
func (r Real) buildDockerImage(drv sandbox.Driver, recipeName string, img recipe.ImageRef) (string, error) {
	ctx := img.Context
	if ctx == "" {
		ctx = filepath.Dir(img.Dockerfile)
	}
	dockerfile, err := r.absPath(img.Dockerfile)
	if err != nil {
		return "", err
	}
	ctxAbs, err := r.absPath(ctx)
	if err != nil {
		return "", err
	}
	if err := drv.Build(sandbox.BuildSpec{Name: recipeName, Dockerfile: dockerfile, Context: ctxAbs}); err != nil {
		return "", err
	}
	return recipeName, nil
}

func (r Real) hasBundledImage(name string) bool {
	_, err := fs.Stat(r.images, "images/"+name)
	return err == nil
}

// absPath makes p absolute against the process working directory, read through
// the data seam so a fake can control it.
func (r Real) absPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd, err := r.data.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, p), nil
}

// isHostRelative reports whether a source origin is a plain relative path — one
// that must be anchored on a directory. The forms expandPath resolves on its own
// (~ and $VAR) are not: this is the single place that knows which is which.
func isHostRelative(p string) bool {
	return p != "" && !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") && !strings.HasPrefix(p, "$")
}

// envRef matches $VAR and ${VAR} references in a path.
var envRef = regexp.MustCompile(`\$\{?(\w+)\}?`)

// expandPath resolves a leading ~ and any $VAR/${VAR} in a host path. An unset
// variable is an error, not a silent truncation to a shorter (wrong) path.
func (r Real) expandPath(p string) (string, error) {
	return r.expandPathWith(p, nil)
}

// expandPathWith expands a source path, resolving names in vars before the
// environment. vars carries what dabs itself supplies (a node's spaces), so a
// recipe can name them without them leaking into the box's environment.
func (r Real) expandPathWith(p string, vars map[string]string) (string, error) {
	// Space vars ($NODE_*/$PARENT_*) name a root dabs owns; a path built from one
	// must stay under that root. Remember the roots a path references so a `..`
	// that climbs above the named space is caught after expansion.
	var roots []struct{ name, root string }
	for _, m := range envRef.FindAllStringSubmatch(p, -1) {
		if root, ok := vars[m[1]]; ok {
			roots = append(roots, struct{ name, root string }{m[1], root})
			continue
		}
		if _, ok := r.data.LookupEnv(m[1]); !ok {
			return "", fmt.Errorf("unset variable %s in source path %q", m[0], p)
		}
	}
	p = os.Expand(p, func(k string) string {
		if v, ok := vars[k]; ok {
			return v
		}
		return r.data.Getenv(k)
	})
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := r.data.HomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	clean := filepath.Clean(p)
	for _, s := range roots {
		if !withinRoot(s.root, clean) {
			return "", fmt.Errorf("source path %q escapes its $%s space (%s) — a dabs space path may not climb out of the space it names", p, s.name, s.root)
		}
	}
	return clean, nil
}

// withinRoot reports whether target is root itself or nested under it, comparing
// on path boundaries so /foo does not appear to contain /foobar. Both are cleaned
// first, so a `..` that climbs above root is rejected.
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// addWorktree provisions a fresh worktree NODE at ~/.dabs/nodes/<repo>-<id>/,
// with the git worktree checked out into its ephemeral space on a new branch
// dabs/<id> off HEAD. The checkout is EPHEMERAL: dabs cut it, so dabs may reap
// it — but `down` asks first when it holds work. It returns the checkout path
// (what the box mounts) and the branch. Requires at least one commit (a born
// HEAD). parent is the node this one stacks on.
func (r Real) addWorktree(top, recipeName, parent string) (path, branch, id string, err error) {
	if !r.data.GitHasCommits(top) {
		return "", "", "", fmt.Errorf("recipe: repo has no commits yet — make an initial commit first")
	}
	id, short := mintNodeID(filepath.Base(top))
	branch = "dabs/" + short

	dir, err := r.resolveNodeSpace(id, SpaceEphemeral)
	if err != nil {
		return "", "", "", fmt.Errorf("recipe: %w", err)
	}
	path = filepath.Join(dir, "worktree")
	// git worktree add creates the checkout dir itself; make only the space above it.
	if err := r.data.MkdirAll(dir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("recipe: node dir: %w", err)
	}
	if err := r.data.GitAddWorktree(top, branch, path); err != nil {
		return "", "", "", fmt.Errorf("recipe: %w", err)
	}
	if err := r.writeNode(Node{
		ID:       id,
		Kind:     KindWorktree,
		Parent:   parent,
		Recipe:   recipeName,
		Created:  stampNow(),
		Worktree: &NodeWorktree{Branch: branch, Repo: top},
	}); err != nil {
		return "", "", "", fmt.Errorf("recipe: %w", err)
	}
	return path, branch, id, nil
}

// isDotSource reports whether a source names `.` — the code the recipe is about,
// as opposed to a dir it happens to also need (a login vault, a skill).
func isDotSource(s recipe.Source) bool {
	_, origin, err := s.Kind()
	return err == nil && origin == "."
}

// ensureProjectNode marks the directory a command ran from — the project, the
// root of every chain and what `.` falls back to. Its Dir is the user's: dabs
// records it and never reaps it.
//
// It is created lazily, by commands that PROVISION something. A read-only
// command (ls, recipes, worktrees) marks nothing, so ~/.dabs/nodes does not grow
// a node for every directory anyone ever ran dabs in.
func (r Real) ensureProjectNode(recipeName string) (string, error) {
	cwd, err := r.data.Getwd()
	if err != nil {
		return "", err
	}
	// dabs's own storage is not a project. Running from inside ~/.dabs would mark
	// dabs's node store as a project whose chain re-renders its own tree.
	if inside, err := r.insideDabsStore(cwd); err != nil {
		return "", err
	} else if inside {
		return "", fmt.Errorf("refusing to provision inside dabs's own storage (%s) — run dabs from your project, not under ~/.dabs", cwd)
	}
	nodes, err := r.listNodes()
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Kind == KindProject && n.Dir == cwd {
			return n.ID, nil
		}
	}
	id, _ := mintNodeID(filepath.Base(cwd))
	if err := r.writeNode(Node{
		ID:      id,
		Kind:    KindProject,
		Recipe:  recipeName,
		Created: stampNow(),
		Dir:     cwd,
	}); err != nil {
		return "", err
	}
	return id, nil
}

// bindWorktree rewrites a recipe's sources to bind an existing dabs worktree
// (by name) to the recipe's `.` origin:
//   - worktree: . / mount: .  → mount the worktree live, PLUS mount its parent
//     .git at its own absolute path so the worktree's `.git` pointer resolves
//     and git works inside the box. `worktree:` prints a note that it attached
//     rather than forking a new branch.
//   - copy: .                 → snapshot the worktree (git stays blind in-box:
//     the object store isn't copied — that's inherent to a copy).
//
// Sources that don't name `.` (a login dir, a skill) pass through untouched.
func (r Real) bindWorktree(recipeName string, in []recipe.Source, worktree string) ([]recipe.Source, error) {
	// The node record is the source of truth for what dabs provisioned — a
	// worktree node has a `worktree` nest. Anything else isn't ours to bind onto.
	n, err := r.readNode(worktree)
	if err != nil {
		return nil, fmt.Errorf("--worktree: no worktree %q (see: dabs worktrees ls)", worktree)
	}
	if n.Worktree == nil {
		return nil, fmt.Errorf("--worktree: %q is not a worktree", worktree)
	}
	wt, err := r.resolveNodeData(worktree)
	if err != nil {
		return nil, err
	}
	gitDir, err := r.data.GitCommonDir(wt)
	if err != nil {
		return nil, fmt.Errorf("--worktree: %s is not a git worktree: %w", wt, err)
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
				fmt.Fprintln(os.Stdout, tui.Muted("--worktree: recipe wants a fresh worktree; binding onto %s — mounting it instead.", wt))
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
		return nil, fmt.Errorf("--worktree: recipe %q has no `.` source to bind the worktree to", recipeName)
	}
	return out, nil
}

// resolveWorktreeArg resolves a `--worktree` argument to a full node id by
// unambiguous prefix, git-style — the same rule every other name dabs takes.
// An exact id wins outright; a unique prefix resolves; an ambiguous prefix lists
// the matches; nothing matching is a plain "no worktree".
func (r Real) resolveWorktreeArg(arg string) (string, error) {
	nodes, err := r.listWorktreeNodes()
	if err != nil {
		return "", err
	}
	var pref []string
	for _, n := range nodes {
		if n.ID == arg {
			return arg, nil
		}
		if strings.HasPrefix(n.ID, arg) {
			pref = append(pref, n.ID)
		}
	}
	switch len(pref) {
	case 0:
		return "", fmt.Errorf("--worktree: no worktree %q (see: dabs worktrees ls)", arg)
	case 1:
		return pref[0], nil
	default:
		sort.Strings(pref)
		return "", fmt.Errorf("--worktree: %q is ambiguous: %s (see: dabs worktrees ls)", arg, strings.Join(pref, ", "))
	}
}

// checkSources rejects source specs that dabs cannot safely realize, BEFORE any
// side effect (no place cut, no image built, no box up). Two contracts:
//
//   - a box PATH (where a source lands inside the box) is a literal absolute path:
//     it carries no variable ($NODE_*/$PARENT_* resolve only in source ORIGINS),
//     is absolute (a relative path is silently rooted at /), and holds no `..`
//     segment (which would escape the declared workdir).
//   - a RELATIVE source ORIGIN stays within the project: a `..` that escapes the
//     cwd would provision a place dabs cannot track or reap. Absolute origins are
//     the user's explicit choice and pass.
//
// A recipe with exactly one `.` source stands on one place; more than one would
// cut several chain tips a single box cannot all parent.
func (r Real) checkSources(recipeName string, sources []recipe.Source, boxless bool) error {
	dots := 0
	for _, s := range sources {
		kind, origin, err := s.Kind()
		if err != nil {
			// A malformed source is reported by validateSources (and shown in the
			// look-before-run summary); leave its message to that path.
			continue
		}
		if origin == "." {
			dots++
		}
		if isHostRelative(origin) && escapesRoot(origin) {
			return fmt.Errorf("recipe %q: source %s:%s escapes the project root with `..` — dabs cannot track or reap a place outside it", recipeName, kind, origin)
		}
		if boxless {
			continue // a boxless recipe makes places; a place has no box path
		}
		if err := checkBoxPath(recipeName, kind, origin, s.Path); err != nil {
			return err
		}
	}
	if dots > 1 {
		return fmt.Errorf("recipe %q: more than one `.` source — a box stands on one place", recipeName)
	}
	return nil
}

// checkBoxPath validates where a source lands inside the box. An empty path is
// left to validateSources (which frames it as "say where it lands").
func checkBoxPath(recipeName, kind, origin, boxPath string) error {
	if boxPath == "" {
		return nil
	}
	if strings.Contains(boxPath, "$") {
		return fmt.Errorf("recipe %q: box path %q for %s:%s uses a variable — $NODE_*/$PARENT_* resolve in source origins, not box paths", recipeName, boxPath, kind, origin)
	}
	if !filepath.IsAbs(boxPath) {
		return fmt.Errorf("recipe %q: box path %q for %s:%s is not absolute — a relative box path is silently rooted at /", recipeName, boxPath, kind, origin)
	}
	if hasDotDot(boxPath) {
		return fmt.Errorf("recipe %q: box path %q for %s:%s uses `..` to escape the workdir", recipeName, boxPath, kind, origin)
	}
	return nil
}

// escapesRoot reports whether a relative path climbs above its anchor with `..`.
func escapesRoot(rel string) bool {
	c := filepath.Clean(rel)
	return c == ".." || strings.HasPrefix(c, "../")
}

// hasDotDot reports whether any segment of a path is `..`.
func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// pathInside reports whether child is parent itself or nested under it.
func pathInside(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// insideDabsStore reports whether dir is ~/.dabs or anything under it — dabs's
// own storage, which must never be marked as a project.
func (r Real) insideDabsStore(dir string) (bool, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(filepath.Join(home, ".dabs"), dir)
	if err != nil {
		return false, nil
	}
	return rel == "." || !strings.HasPrefix(rel, ".."), nil
}

// randHex returns 2n hex chars of cryptographic randomness for naming.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// buildImageIfMissing builds the image named imageName from the bundled recipe
// (images/<provider>), unless the driver reports it is already built — so
// repeated runs skip the redundant rebuild.
func (r Real) buildImageIfMissing(drv sandbox.Driver, provider, imageName string) error {
	built, err := drv.HasImage(imageName)
	if err != nil {
		return err
	}
	if built {
		return nil
	}
	ctxDir, err := r.stageImage(provider)
	if err != nil {
		return err
	}
	defer r.data.RemoveAll(ctxDir)
	return drv.Build(sandbox.BuildSpec{
		Name:       imageName,
		Dockerfile: filepath.Join(ctxDir, "Dockerfile"),
		Context:    ctxDir,
	})
}

// stageImage materializes a bundled image recipe into a temp directory the
// driver can build from.
func (r Real) stageImage(provider string) (string, error) {
	sub := "images/" + provider
	dir, err := r.data.MkdirTemp("", "dabs-image-"+provider+"-")
	if err != nil {
		return "", fmt.Errorf("image: stage: %w", err)
	}
	err = fs.WalkDir(r.images, sub, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(sub, p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return r.data.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(r.images, p)
		if err != nil {
			return err
		}
		return r.data.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		r.data.RemoveAll(dir)
		return "", fmt.Errorf("image: stage %s: %w", sub, err)
	}
	return dir, nil
}
