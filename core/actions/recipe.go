package actions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v2"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/proxy"
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
		return r.upDetached(arg, p.Worktree, p.NodeName)
	}
	reg, err := r.loadRegistry()
	if err != nil {
		return err
	}
	if p.Name != "" {
		return r.runRecipe(reg, p.Name, p.Worktree, nil, false, p.NodeName)
	}
	if p.Default || len(p.Args) == 0 {
		name := reg.Default
		if name == "" {
			name = "sh" // no project default → the bundled generic shell box
		}
		return r.runRecipe(reg, name, "", p.Args, true, p.NodeName)
	}
	name := p.Args[0]
	// A first positional whose SHAPE is a path names a dabs.yaml to load and run,
	// the same resolution `--detach` and `build` use — so `dabs recipe .` runs the
	// recipe in the cwd's dabs.yaml instead of being rejected as an unknown name.
	if _, ok := reg.Recipes[name]; !ok && looksLikePath(name) {
		pathReg, pathName, err := r.resolveRecipe(name)
		if err != nil {
			return err
		}
		return r.runRecipe(pathReg, pathName, p.Worktree, p.Args[1:], false, p.NodeName)
	}
	if _, ok := reg.Recipes[name]; !ok {
		return fmt.Errorf("no recipe %q (known: %s) — or `dabs recipe -- %s` to run it as a command on the default recipe", name, strings.Join(reg.Names(), ", "), name)
	}
	return r.runRecipe(reg, name, p.Worktree, p.Args[1:], false, p.NodeName)
}

// runRecipe is the shared engine behind `dabs recipe` (with or without
// `--worktree`): it ensures the image exists, prepares the recipe's sources (live
// mounts, fresh git worktrees, and up-time copies), brings up a box with them,
// runs the recipe's command interactively, and tears the box down on exit.
// Worktrees are KEPT (paths printed) so no in-box work is silently discarded.
// Everything the box does is declared in the recipe. `extra` is appended to the
// recipe's command; when it is non-empty the caller must first approve the
// recipe and the exact command (nothing is built or run until they do).
func (r Real) runRecipe(reg recipe.Registry, name, worktree string, extra []string, alwaysConfirm bool, nodeName string) error {
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
		return r.provisionNodes(name, rec, worktree, nodeName)
	}
	// An empty recipe command has nothing to run, and appended argv cannot supply
	// one: it is appended to the recipe's command, so with no command it would
	// reach the driver as bare options (bwrap: Unknown option). The recipe's own
	// command is what defines the run.
	if len(rec.Command) == 0 {
		return fmt.Errorf("recipe %q: no command to run", name)
	}
	// Booting a box from inside a dabs worktree's own checkout (a cwd under
	// ~/.dabs/nodes/<id>/held/worktree) parents the box on that worktree, exactly
	// as an explicit --worktree would, instead of trying to mark the checkout as a
	// new project. An explicit --worktree wins.
	if worktree == "" {
		owner, oerr := r.cwdOwningWorktree()
		if oerr != nil {
			return oerr
		}
		worktree = owner
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
	// The claim runs after the confirm and after every name-independent
	// refusal — including the PURE image check below: an image that can never
	// be had refuses while nothing has happened yet. The image BUILD stays
	// after source validation (a bad source must fail before any docker build
	// runs), so a failed build can still follow a successful claim — like a
	// failed boot, that is a death mid-flight, and the reservation's claim
	// marker lets the next attempt reclaim the dir (see reserveNodeDir).
	if nodeName != "" {
		if err := r.preflightImage(drv, name, rec.Image); err != nil {
			return err
		}
		if err := r.claimNodeName(nodeName); err != nil {
			return err
		}
	}

	// Cut the PLACE first: a box names its parent's spaces ($PARENT_VOLUME), and a
	// parent must exist to be named.
	_, tip, hosts, kept, cut, err := r.provisionPlaces(name, snapshotRecipe(rec), sources, worktree)
	if err != nil {
		return err
	}
	boxID, vars, err := r.mintBoxNode(name, tip, nodeName)
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
	fmt.Fprintf(os.Stdout, "%s %s\n\n", tui.Muted("running:"), shellJoin(command))
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
func (r Real) provisionNodes(name string, rec recipe.Recipe, worktree, nodeName string) error {
	if worktree != "" {
		return fmt.Errorf("recipe %q: has no image, so there is no box to put onto a worktree", name)
	}
	// With no box, the place IS the leaf a --name names. One name, one node: a
	// recipe declaring several places cannot spread one name over them.
	if nodeName != "" {
		places := 0
		for _, s := range rec.Sources {
			if kind, _, err := s.Kind(); err == nil && (kind == "worktree" || kind == "copy") {
				places++
			}
		}
		if places == 0 {
			return fmt.Errorf("recipe %q: has no image and no source that makes a place — nothing for --name %s to name", name, nodeName)
		}
		if places > 1 {
			return fmt.Errorf("recipe %q: --name %s names ONE node, and this recipe provisions %d places", name, nodeName, places)
		}
		// Claimed only once every other refusal has passed: a boot refused
		// above has not touched the name's holder.
		if err := r.claimNodeName(nodeName); err != nil {
			return err
		}
	}
	project, err := r.ensureProjectNode(name)
	if err != nil {
		return err
	}
	// One resolved snapshot for whatever leaf nodes this boxless recipe makes —
	// their creation-time provenance, so `dabs info` reads what actually
	// provisioned them, not a registry that may have drifted since.
	snap := snapshotRecipe(rec)
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
			wt, branch, id, err := r.addWorktree(top, name, snap, project, nodeName)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "%s %s %s %s\n", tui.Success("worktree"), tui.Accent(id), tui.Muted("branch "+branch+" ·"), tui.Muted(wt))
			made++
		case "copy":
			id, _ := mintNodeID(filepath.Base(host))
			if nodeName != "" {
				id = nodeName
			}
			dir, err := r.workdirData(id)
			if err != nil {
				return err
			}
			if err := r.data.CopyDir(host, dir); err != nil {
				return fmt.Errorf("recipe %q: copy %s: %w", name, host, err)
			}
			// Dir is the copy's own location — where it lives, not its source.
			if err := r.writeNode(Node{ID: id, Kind: KindWorkdir, Parent: project, Recipe: name, RecipeSpec: snap, Created: stampNow(), Dir: dir}); err != nil {
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
// in the node's held space, so `rm` asks before reaping it and you can
// read it on the host.
func (r Real) workdirData(id string) (string, error) {
	eph, err := r.resolveNodeSpace(id, SpaceHeld)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(eph, "work")
	return dir, r.data.MkdirAll(dir, 0o755)
}

// mintBoxNode names the box's node before the box exists, and returns the three
// space paths a recipe may name as $NODE_VOLUME / $NODE_HELD / $NODE_TMP.
// The id is minted first because a source may mount a space, and a mount needs a
// path before the driver is called. nodeName, when set, IS the id — the box is
// the boot's leaf, so a --name lands here (claimed by the caller already).
func (r Real) mintBoxNode(recipeName, parent, nodeName string) (id string, vars map[string]string, err error) {
	id, _ = mintNodeID(recipeName)
	if nodeName != "" {
		id = nodeName
	}
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

// spaceVars names a node's three spaces under a prefix, for source paths. The
// held space is offered under two names: $<prefix>_HELD (the documented one) and
// $<prefix>_EPHEMERAL (a PERMANENT alias for the space's former name, so a user's
// own recipes.yaml written against the old name keeps provisioning into the same
// held space and is never broken by the rename). Both resolve to held/.
func (r Real) spaceVars(id, prefix string) (map[string]string, error) {
	vars := map[string]string{}
	for v, space := range map[string]string{
		"_VOLUME":    SpaceVolume,
		"_HELD":      SpaceHeld,
		"_EPHEMERAL": SpaceHeld, // permanent alias for _HELD (the former name)
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
// node's own spaces — so the recipe, not this function, decides what `rm` may
// reap.
func (r Real) provisionPlaces(recipeName string, snap *recipe.Recipe, sources []recipe.Source, boundWorktree string) (project, tip string, hosts map[int]string, kept []string, cut []wtCut, err error) {
	if boundWorktree != "" {
		// --worktree (given explicitly, or the cwd sitting inside a worktree's own
		// checkout) binds an EXISTING place; bindWorktree already rewrote the `.`
		// source to mount it, so there is nothing to provision and the tip is that
		// node. The chain root is the worktree's own project ancestor — resolved
		// here rather than from the cwd, which is the checkout itself.
		n, rerr := r.readNode(boundWorktree)
		if rerr != nil {
			return "", "", nil, nil, nil, rerr
		}
		root := n.Parent
		if root == "" {
			root = boundWorktree
		}
		return root, boundWorktree, map[int]string{}, nil, nil, nil
	}
	project, err = r.ensureProjectNode(recipeName)
	if err != nil {
		return "", "", nil, nil, nil, err
	}
	tip, hosts = project, map[int]string{}
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
			if err := r.cutWorktree(top, branch, at, id, recipeName, snap, project); err != nil {
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
			if err := r.writeNode(Node{ID: id, Kind: KindWorkdir, Parent: project, Recipe: recipeName, RecipeSpec: snap, Created: stampNow(), Dir: at}); err != nil {
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
// NEW node's spaces. Unset, it is that node's held space: dabs made it, so
// dabs may reap it, but `rm` asks first because that is where work lives.
func (r Real) placeAt(s recipe.Source, id, leaf string) (string, error) {
	vars, err := r.spaceVars(id, "NODE")
	if err != nil {
		return "", err
	}
	if s.At == "" {
		return filepath.Join(vars["NODE_HELD"], leaf), nil
	}
	return r.expandPathWith(s.At, vars)
}

// cutWorktree checks out a new branch off HEAD into at, and records the node.
func (r Real) cutWorktree(top, branch, at, id, recipeName string, snap *recipe.Recipe, parent string) error {
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
		ID:         id,
		Kind:       KindWorktree,
		Parent:     parent,
		Recipe:     recipeName,
		RecipeSpec: snap,
		Created:    stampNow(),
		Dir:        at,
		Worktree:   &NodeWorktree{Branch: branch, Repo: top},
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
	// A non-empty proxies chain makes the engine's socket the box's only way out:
	// start it, mount its CA, point the box's HTTP_PROXY at the in-box forwarder.
	// The engine runs for the box's lifetime; its PID/dir are recorded on the box
	// node below so the box's down path (teardown or `dabs rm`) reaps it.
	// Egress is chosen by the recipe's mode (default open = full outbound): open
	// leaves the box's network alone, none cuts it entirely, proxy routes it
	// through the recipe's proxies chain (the driver enforces none/proxy). The
	// recipe validator already guaranteed the mode↔chain consistency.
	env := rec.Env
	egressMode, proxySock, forwarderBin := "", "", ""
	proxyPID, proxyDir := 0, ""
	switch rec.EgressMode() {
	case recipe.EgressProxy:
		p, perr := proxy.Provision(drv, recipeName, rec.Egress, rec.Env, r.expandPath)
		if perr != nil {
			return "", perr
		}
		env = p.Env
		mounts = append(mounts, p.Mounts...)
		egressMode, proxySock, forwarderBin = sandbox.EgressProxy, p.Socket, p.ForwarderBin
		proxyPID, proxyDir = p.PID, p.Dir
		// The engine is live now but the box node that carries its PID/dir is not
		// written until the box is up. If any step between here and writeNode fails
		// (drv.Up, the smoke check, writeNode itself), nothing else can reap the
		// engine — no node records it. Reap it on a failed return.
		defer func() {
			if err != nil {
				proxy.Reap(proxyPID, proxyDir)
			}
		}()
	case recipe.EgressNone:
		egressMode = sandbox.EgressNone // driver cuts the box's network
	case recipe.EgressOpen:
		egressMode = sandbox.EgressOpen // full outbound; nothing to provision
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
	instance, err = drv.Up(sandbox.Spec{Name: image, Workdir: workdir, Env: env, Mounts: mounts, Egress: egressMode, ProxySock: proxySock, ForwarderBin: forwarderBin})
	if err != nil {
		return "", err
	}

	// Mark the box: the node was named before the box came up (its spaces had to
	// exist to be mounted), so record which sandbox it turned out to be. A proxy
	// engine's PID/dir ride along so the box's down path can reap it.
	box := Node{
		ID:         boxID,
		Kind:       KindBox,
		Parent:     tip,
		Recipe:     recipeName,
		RecipeSpec: snapshotRecipe(rec),
		Created:    stampNow(),
		Instance:   instance,
	}
	box.ProxyPID, box.ProxyDir = proxyPID, proxyDir
	if err := r.writeNode(box); err != nil {
		return "", err
	}

	// The box exists now, so its instance can be journalled against each fresh
	// worktree it was cut for (best-effort; a log failure only warns).
	for _, c := range cut {
		r.logWorktreeUp(instance, c.name, c.path, recipeName)
	}

	return instance, nil
}

// snapshotRecipe returns a DEEP copy of a resolved recipe, to persist on a node
// as its creation-time provenance (Node.RecipeSpec). A plain struct copy would
// alias the recipe's slices and maps (Command, Env, Sources, the egress chain),
// which is safe only while the write follows immediately; a later refactor that
// deferred or mutated could then corrupt the snapshot. A JSON round-trip — the
// very form writeNode persists — copies every level. A marshal error (never
// expected for a recipe dabs just resolved) degrades to the shallow copy rather
// than failing the provision: a snapshot is provenance, not a gate.
func snapshotRecipe(rec recipe.Recipe) *recipe.Recipe {
	b, err := json.Marshal(rec)
	if err != nil {
		return &rec
	}
	var clone recipe.Recipe
	if err := json.Unmarshal(b, &clone); err != nil {
		return &rec
	}
	return &clone
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
	// An arg whose SHAPE is a path is a dabs.yaml to load and take the default (or
	// sole recipe) from — resolved against the process cwd when relative. Path
	// shape, not a stat probe, decides this: a bare word is never guessed onto
	// disk (so a recipe named `foo` still resolves when a `foo/` dir sits in the
	// cwd), and a path that is missing errors AS a path, not as an unknown name.
	if looksLikePath(arg) {
		path := arg
		if fi, statErr := r.data.Stat(arg); statErr == nil && fi != nil && fi.IsDir() {
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
// worktree) AND its proxy hook `module:` paths to dir, for a dabs.yaml loaded BY
// PATH — the same rule rebaseImagePaths applies to its image, so `dabs recipe
// path/to/box --detach` provisions the same box from any cwd. Absolute origins,
// `~`/`$VAR` origins (expanded later), and `perbox:` labels are left alone.
//
// Registry recipes (bundled, ~/.dabs/recipes.yaml, ./dabs.yaml) are NOT rebased:
// their relative origins stay cwd-relative, which is what `mount: .` = "your
// cwd, live" means. For a project ./dabs.yaml the two are the same directory. A
// proxy module resolves exactly like a source: alongside a dabs.yaml run by
// path, cwd-relative for a project ./dabs.yaml you invoke from its own root.
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
		if hops := rec.Egress.HTTPProxy; len(hops) > 0 {
			rebased := make([]recipe.ProxyHop, len(hops))
			copy(rebased, hops)
			for i := range rebased {
				rebased[i].Module = rebase(rebased[i].Module)
			}
			rec.Egress.HTTPProxy = rebased
		}
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

// Recipes lists the known recipes, one line each: the recipe name, its
// description, and its origin — so a user (or agent) can see what recipes
// exist, and which layer defined each, at a glance.
func (r Real) Recipes(p params.Recipes) error {
	reg, origins, err := r.loadRegistryOrigins()
	if err != nil {
		return err
	}
	// --print dumps the full MERGED registry — bundled, ~/.dabs/recipes.yaml
	// and ./dabs.yaml, later winning — as YAML, each recipe under a comment
	// naming its origin, so "what does recipe X mount?" is answerable from the
	// CLI whichever layer defined X. A name prints just that recipe.
	if p.Print {
		return printRecipes(reg, origins, p.Name)
	}
	if p.Name != "" {
		return fmt.Errorf("recipes: a name goes with --print (recipes --print %s)", p.Name)
	}
	names := reg.Names()
	if len(names) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("no recipes"))
		return nil
	}
	rows := make([][]string, 0, len(names))
	for _, n := range names {
		// "%s" keeps a user-authored description inert: Muted formats its
		// arguments, and a bare % in a description is content, not a verb.
		desc := tui.Muted("%s", reg.Recipes[n].Description)
		if n == reg.Default {
			if reg.Recipes[n].Description == "" {
				desc = tui.Badge("default")
			} else {
				desc += " " + tui.Badge("default")
			}
		}
		rows = append(rows, []string{tui.Accent(n), desc, tui.Muted("%s", origins[n])})
	}
	fmt.Fprintln(os.Stdout, tui.Rows(nil, rows))
	return nil
}

// originLabel names a registry layer for a human: which file to edit to change
// the recipe.
func originLabel(origin string) string {
	switch origin {
	case originGlobal:
		return "global (~/.dabs/recipes.yaml)"
	case originProject:
		return "project (./dabs.yaml)"
	default:
		return originBundled
	}
}

// printRecipes renders the merged registry (or one recipe of it) as YAML, each
// recipe preceded by a comment naming the layer that defined it.
func printRecipes(reg recipe.Registry, origins map[string]string, name string) error {
	names := reg.Names()
	if name != "" {
		if _, ok := reg.Recipes[name]; !ok {
			return fmt.Errorf("no recipe %q (known: %s)", name, strings.Join(names, ", "))
		}
		names = []string{name}
	}
	if reg.Default != "" && name == "" {
		fmt.Fprintf(os.Stdout, "default: %s\n", reg.Default)
	}
	fmt.Fprintln(os.Stdout, "recipes:")
	for _, n := range names {
		b, err := yaml.Marshal(map[string]recipe.Recipe{n: reg.Recipes[n]})
		if err != nil {
			return fmt.Errorf("recipes: %s: %w", n, err)
		}
		fmt.Fprintf(os.Stdout, "  # %s\n", originLabel(origins[n]))
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			fmt.Fprintln(os.Stdout, "  "+line)
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
	fmt.Fprintf(&b, "command: %s", shellJoin(command))
	return b.String()
}

// shellJoin renders an argv for human eyes with shell-style quoting, so the
// token boundaries survive: `sh -c 'echo hi'` reads as three arguments, not
// four. Args holding whitespace, shell metacharacters, or nothing at all are
// single-quoted (embedded single quotes escaped the POSIX way).
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n\"'`$&|;<>()*?[]#~\\") {
			quoted[i] = a
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

type copyOp struct{ src, dest string }

// wtCut is a fresh worktree buildBox cut, held until the box is up so its
// instance can be journalled against the worktree's name and absolute path.
type wtCut struct{ name, path string }

// The three registry layers a recipe can come from, later winning by name.
const (
	originBundled = "bundled"
	originGlobal  = "global"
	originProject = "project"
)

// loadRegistry builds the effective registry: bundled defaults, overlaid by the
// user's ~/.dabs/recipes.yaml, overlaid by the project's ./dabs.yaml. Later
// sources win (recipes by name, and `default`). Missing files are fine.
func (r Real) loadRegistry() (recipe.Registry, error) {
	reg, _, err := r.loadRegistryOrigins()
	return reg, err
}

// loadRegistryOrigins is loadRegistry keeping the provenance: for each recipe
// name, which layer defined the WINNING entry — bundled, global
// (~/.dabs/recipes.yaml), or project (./dabs.yaml).
func (r Real) loadRegistryOrigins() (recipe.Registry, map[string]string, error) {
	reg, err := recipe.Parse(recipe.Bundled)
	if err != nil {
		return recipe.Registry{}, nil, fmt.Errorf("recipe: bundled registry: %w", err)
	}
	origins := make(map[string]string, len(reg.Recipes))
	for n := range reg.Recipes {
		origins[n] = originBundled
	}
	if home, err := r.data.HomeDir(); err == nil {
		if err := r.mergeRecipeFile(&reg, origins, originGlobal, filepath.Join(home, ".dabs", "recipes.yaml")); err != nil {
			return reg, origins, err
		}
	}
	if err := r.mergeRecipeFile(&reg, origins, originProject, "dabs.yaml"); err != nil { // project-local, cwd
		return reg, origins, err
	}
	return reg, origins, nil
}

// mergeRecipeFile overlays a recipes file onto reg if it exists, marking each
// recipe it defines with the layer's origin.
func (r Real) mergeRecipeFile(reg *recipe.Registry, origins map[string]string, origin, path string) error {
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
	for n := range parsed.Recipes {
		origins[n] = origin
	}
	reg.Merge(parsed)
	return nil
}

// ensureImage makes the recipe's image available and returns the name to run.
// It reuses an already-built image only when the SOURCE it was built from is
// unchanged (see ensureImageFresh); a changed or unrecorded source rebuilds.
func (r Real) ensureImage(drv sandbox.Driver, recipeName string, img recipe.ImageRef) (string, error) {
	name, _, err := r.ensureImageFresh(drv, recipeName, img)
	return name, err
}

// ensureImageFresh makes the recipe's image available, returns the name to run,
// and reports whether it (re)built. It reuses a built image only when its
// recorded source digest matches the current source; a changed digest or a
// missing record (a legacy image, or one never built by this dabs) rebuilds and
// SAYS why, so an agent reading the output can see a build ran and why.
//
//   - inline {dockerfile,context}: digest-gated on the Dockerfile bytes, built
//     as the recipe's own name.
//   - a BUNDLED bare name (images/<name>): digest-gated on the embedded files,
//     built from the bundled recipe — this is what self-heals a stale bundled
//     image (curl added to images/shell).
//   - a NON-bundled bare name (staged/pulled elsewhere, no source dabs owns):
//     reused if present, else reported missing — there is nothing here to digest
//     or rebuild from.
func (r Real) ensureImageFresh(drv sandbox.Driver, recipeName string, img recipe.ImageRef) (name string, built bool, err error) {
	if img.Dockerfile != "" {
		digest, err := r.currentInlineDigest(img)
		if err != nil {
			return "", false, err
		}
		stale, reason, err := r.imageReuse(drv, recipeName, digest)
		if err != nil {
			return "", false, err
		}
		if stale == imageFresh {
			return recipeName, false, nil
		}
		r.sayRebuild(reason)
		if _, err := r.buildDockerImage(drv, recipeName, img); err != nil {
			if r.serveUnbuildable(recipeName, stale, err) {
				return recipeName, false, nil
			}
			return "", false, err
		}
		return recipeName, true, nil
	}
	name = img.Name
	if name == "" {
		return "", false, fmt.Errorf("recipe %q: image has no name and no dockerfile", recipeName)
	}
	if !r.hasBundledImage(name) {
		built, err := drv.HasImage(name)
		if err != nil {
			return "", false, err
		}
		if built {
			return name, false, nil
		}
		return "", false, fmt.Errorf("recipe %q: image %q is not built and dabs has no bundled recipe for it", recipeName, name)
	}
	digest, err := r.currentBundledDigest(name)
	if err != nil {
		return "", false, err
	}
	stale, reason, err := r.imageReuse(drv, name, digest)
	if err != nil {
		return "", false, err
	}
	if stale == imageFresh {
		return name, false, nil
	}
	r.sayRebuild(reason)
	if err := r.buildBundledImage(drv, name, digest); err != nil {
		if r.serveUnbuildable(name, stale, err) {
			return name, false, nil
		}
		return "", false, err
	}
	return name, true, nil
}

// preflightImage is the check-only half of ensureImage: it refuses an image
// that can NEVER be had — a bare name that is neither built nor bundled, with
// nothing to build it from. It builds nothing and records nothing, so it may
// run before a name is claimed; whether a buildable image is fresh stays
// ensureImage's question, asked after source validation.
func (r Real) preflightImage(drv sandbox.Driver, recipeName string, img recipe.ImageRef) error {
	if img.Dockerfile != "" {
		return nil // buildable: ensureImage's problem, after validation
	}
	if img.Name == "" {
		return fmt.Errorf("recipe %q: image has no name and no dockerfile", recipeName)
	}
	if r.hasBundledImage(img.Name) {
		return nil // buildable from the bundled recipe
	}
	built, err := drv.HasImage(img.Name)
	if err != nil {
		return err
	}
	if !built {
		return fmt.Errorf("recipe %q: image %q is not built and dabs has no bundled recipe for it", recipeName, img.Name)
	}
	return nil
}

// serveUnbuildable reports whether an image a rebuild wanted may be served
// as-is instead. Three conditions, all required: the build was refused because
// the host carries no builder (sandbox.ErrNoBuilder); the image EXISTS; and the
// rebuild was wanted only because there is NO build record. That last one is
// the STAGED image case — a rootfs placed in the image dir by something other
// than a dabs build (a nested-sandboxing box's Dockerfile stages `shell` this
// way), where the rebuild can never run and the image the host holds is the
// only one there will ever be. A record that says the source CHANGED is a
// contradiction on file: serving that image would hand out a build known to be
// stale, so the boot fails instead. Any other build failure is not served
// around either.
func (r Real) serveUnbuildable(name string, stale imageStaleness, buildErr error) bool {
	if stale != imageNoRecord || !errors.Is(buildErr, sandbox.ErrNoBuilder) {
		return false
	}
	fmt.Fprintln(os.Stdout, tui.Muted("image %s: no builder here to refresh it — serving it as-is", name))
	return true
}

// sayRebuild prints why a rebuild is happening (empty reason: a plain first
// build, which needs no explanation).
func (r Real) sayRebuild(reason string) {
	if reason != "" {
		fmt.Fprintln(os.Stdout, tui.Muted(reason))
	}
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
	// Record the source this image was built from, so a later boot can tell a
	// stale image (edited Dockerfile) from a fresh one. Best-effort: a record
	// failure only costs one extra rebuild next time, never a stale reuse.
	if digest, derr := r.currentInlineDigest(img); derr == nil {
		if err := r.recordImageDigest(recipeName, digest); err != nil {
			fmt.Fprintln(os.Stderr, tui.Warn("image %s: could not record build digest (%v) — it will rebuild next time", recipeName, err))
		}
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

// looksLikePath reports whether a recipe-or-path argument names a dabs.yaml on
// disk rather than a recipe in the registry. It keys on SHAPE alone so a bare
// word is never guessed onto the filesystem: an argument is a path when it is
// absolute, contains a path separator, is exactly ".", starts with "./", "../"
// or "~", or ends in "dabs.yaml". Everything else is a recipe name.
func looksLikePath(arg string) bool {
	return filepath.IsAbs(arg) ||
		strings.ContainsRune(arg, filepath.Separator) ||
		arg == "." ||
		strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "../") ||
		strings.HasPrefix(arg, "~") ||
		strings.HasSuffix(arg, "dabs.yaml")
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
// with the git worktree checked out into its held space on a new branch
// dabs/<id> off HEAD. The checkout is HELD: dabs cut it, so dabs may reap
// it — but `rm` asks first when it holds work. It returns the checkout path
// (what the box mounts) and the branch. Requires at least one commit (a born
// HEAD). parent is the node this one stacks on.
func (r Real) addWorktree(top, recipeName string, snap *recipe.Recipe, parent, nodeName string) (path, branch, id string, err error) {
	if !r.data.GitHasCommits(top) {
		return "", "", "", fmt.Errorf("recipe: repo has no commits yet — make an initial commit first")
	}
	id, short := mintNodeID(filepath.Base(top))
	if nodeName != "" {
		// The chosen name is the node's one handle, and the branch carries it
		// too: dabs/<name>, the same shape minted worktrees get.
		id, short = nodeName, nodeName
	}
	branch = "dabs/" + short

	dir, err := r.resolveNodeSpace(id, SpaceHeld)
	if err != nil {
		return "", "", "", fmt.Errorf("recipe: %w", err)
	}
	path = filepath.Join(dir, "worktree")
	// git worktree add creates the checkout dir itself; make only the space above it.
	if err := r.data.MkdirAll(dir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("recipe: node dir: %w", err)
	}
	if err := r.data.GitAddWorktree(top, branch, path); err != nil {
		// The node dir was made (or reserved by the claim) but holds no record
		// yet: take it back out, so a failed cut never litters a recordless dir.
		if nd, derr := r.resolveNodeDir(id); derr == nil {
			_ = r.data.RemoveAll(nd)
		}
		return "", "", "", fmt.Errorf("recipe: %w", err)
	}
	if err := r.writeNode(Node{
		ID:         id,
		Kind:       KindWorktree,
		Parent:     parent,
		Recipe:     recipeName,
		RecipeSpec: snap,
		Created:    stampNow(),
		Worktree:   &NodeWorktree{Branch: branch, Repo: top},
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

// ensureProjectNode marks the directory a command ran from — the root of the
// chain and what `.` falls back to. Its Dir is the user's: dabs records it and
// never reaps it.
//
// The marker's KIND is what the directory IS. A plain directory (or a repo's
// main checkout) is a PROJECT. A LINKED git worktree is a WORKTREE: the marker
// is kind worktree with Dir the checkout and no held checkout of its own (the
// bytes are externally managed — dabs neither cut them nor reaps them, and rm's
// unreviewed-work guard does not apply). Its parent is the repo's main-checkout
// project when dabs tracks one, else it roots its own chain — the one place the
// project → (workdir|worktree)? → box chain starts below a project. A stale
// PROJECT record whose dir is a linked worktree (written by an older dabs) is
// still reused as the chain root and reaps normally.
//
// It is created lazily, by commands that PROVISION something. A read-only
// command (ls, recipes, worktrees) marks nothing, so ~/.dabs/nodes does not grow
// a node for every directory anyone ever ran dabs in.
func (r Real) ensureProjectNode(recipeName string) (string, error) {
	cwd, err := r.data.Getwd()
	if err != nil {
		return "", err
	}
	// dabs's own storage is not a project. Marking a node store dir as a project
	// would re-render dabs's own tree; a worktree of a worktree's checkout, or a
	// scratch copy of dabs's storage, is nonsense. Booting a box from inside a
	// worktree's checkout is the one allowed case, and it never reaches here — the
	// caller resolves the owning worktree first and binds onto it.
	if inside, err := r.insideDabsStore(cwd); err != nil {
		return "", err
	} else if inside {
		return "", fmt.Errorf("refusing to make a project, worktree, or scratch node inside dabs's own storage (%s) — run dabs from your project, not under ~/.dabs (booting a box from a worktree's checkout is the exception, and works)", cwd)
	}
	nodes, err := r.listNodes()
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Dir == cwd && (n.Kind == KindProject || (n.Kind == KindWorktree && n.Worktree == nil)) {
			return n.ID, nil
		}
	}
	kind := KindProject
	parent := ""
	if top, terr := r.data.GitToplevel(cwd); terr == nil {
		if cd, cerr := r.data.GitCommonDir(cwd); cerr == nil && filepath.Dir(cd) != top {
			kind = KindWorktree
			main := filepath.Dir(cd) // the common dir is <main checkout>/.git
			for _, n := range nodes {
				if n.Kind == KindProject && n.Dir == main {
					parent = n.ID
					break
				}
			}
		}
	}
	// A chain-root node's name is a pure function of the directory it marks, so
	// every boot racing in one directory computes the SAME name, and the
	// exclusive create of that node dir is the lock: exactly one boot mints the
	// node, the others find the dir taken and read the winner's record.
	id := projectNodeID(cwd)
	root, err := r.resolveNodesRoot()
	if err != nil {
		return "", err
	}
	if err := r.data.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dir, err := r.resolveNodeDir(id)
	if err != nil {
		return "", err
	}
	if err := r.data.Mkdir(dir, 0o755); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		// Another boot holds the lock; its record lands right after the dir.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if n, err := r.readNode(id); err == nil {
				return n.ID, nil
			}
		}
		return "", fmt.Errorf("node %s: its dir exists but no readable record appeared", id)
	}
	if err := r.writeNode(Node{
		ID:      id,
		Kind:    kind,
		Parent:  parent,
		Recipe:  recipeName,
		Created: stampNow(),
		Dir:     cwd,
	}); err != nil {
		return "", err
	}
	return id, nil
}

// projectNodeID names a project node deterministically from the directory it
// marks: <basename>-<8 hex of sha256(path)>. Determinism is what makes
// ensureProjectNode race-free — concurrent boots in one directory all reach for
// one name — and the hash keeps same-named directories in different places
// distinct.
func projectNodeID(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return filepath.Base(dir) + "-" + hex.EncodeToString(sum[:4])
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

// cwdOwningWorktree resolves the cwd to the dabs worktree node whose checkout
// contains it, or "" when the cwd is not inside any worktree's checkout. A cwd
// outside dabs's own storage never resolves here — it provisions places the
// ordinary way; only a cwd under ~/.dabs (e.g. a worktree checkout at
// ~/.dabs/nodes/<id>/held/worktree) is matched against the worktrees dabs owns.
// It is what lets a box booted from inside a worktree's checkout parent on that
// worktree instead of trying to mark the checkout as a new project.
func (r Real) cwdOwningWorktree() (string, error) {
	cwd, err := r.data.Getwd()
	if err != nil {
		return "", err
	}
	inside, err := r.insideDabsStore(cwd)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", nil
	}
	nodes, err := r.listWorktreeNodes()
	if err != nil {
		return "", err
	}
	best, bestLen := "", -1
	for _, n := range nodes {
		data, derr := r.resolveNodeData(n.ID)
		if derr != nil {
			continue
		}
		// The deepest checkout wins, so a worktree nested under another's checkout
		// resolves to the nearer node rather than an ancestor.
		if pathInside(cwd, data) && len(data) > bestLen {
			best, bestLen = n.ID, len(data)
		}
	}
	return best, nil
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

// buildBundledImage builds a bundled image from its embedded recipe
// (images/<name>) and records the source digest it was built from, so a later
// boot can tell a stale bundled image (changed embedded files) from a fresh one.
// The caller has already decided a build is warranted, so this does not re-check
// existence.
func (r Real) buildBundledImage(drv sandbox.Driver, name, digest string) error {
	ctxDir, err := r.stageImage(name)
	if err != nil {
		return err
	}
	defer r.data.RemoveAll(ctxDir)
	if err := drv.Build(sandbox.BuildSpec{
		Name:       name,
		Dockerfile: filepath.Join(ctxDir, "Dockerfile"),
		Context:    ctxDir,
	}); err != nil {
		return err
	}
	if err := r.recordImageDigest(name, digest); err != nil {
		fmt.Fprintln(os.Stderr, tui.Warn("image %s: could not record build digest (%v) — it will rebuild next time", name, err))
	}
	return nil
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
