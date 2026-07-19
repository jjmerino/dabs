# AGENTS.md — running things in a dabs box

You are (presumably) a capable agent with host access. dabs lets you run a
command — or a whole agent — inside a disposable box that sees only what its
recipe mounts in, not the rest of your host. Reach in with `dabs exec <node>
<shell…>` (or `dabs exec <node> -- <cmd>` for an exact argv), or run a whole
agent inside via a recipe
(`dabs recipe claude`, defined in this repo's `dabs.yaml`).

## Read `dabs.yaml` first

Before you run or test anything with dabs in this repo, **read `./dabs.yaml`**.
It decides what every bare command does. Nothing below is meaningful until you
know what is in it:

- **`default:`** is what `dabs build`, `dabs recipe --detach`, and `dabs recipe`
  resolve to when you pass no name — and, for `recipe`, also when the first token
  is not a known recipe (then ALL tokens are appended to the default's command).
  It is NOT a shell. A `default:` naming a `claude -p` agent turns a bare
  `dabs recipe -c 'echo hi'` into your argv appended to Claude's — an agent that
  boots and prints nothing for minutes. This repo sets no `default:`, so
  `build`/`recipe --detach` with no name list the choices, and `recipe` with an
  unknown/absent name falls back to the bundled `sh` box. Name the recipe you mean:
  `dabs recipe sh -c 'echo hi'`.
- **Which recipes exist**. `dabs recipes` lists them one line each — name,
  description, and origin (bundled | global `~/.dabs/recipes.yaml` | project
  `./dabs.yaml`). Full detail (image, command, sources): `dabs recipes --print`
  dumps the whole MERGED registry as YAML, each recipe marked with its origin,
  and `dabs recipes --print <name>` dumps one recipe — mounts and all.

## The loop

1. **Build the box image** (once per Dockerfile change) — `build` resolves a
   RECIPE (no name → the registry `default:`, a name → that recipe, a path → a
   `dabs.yaml` to load) and builds its image:

   ```bash
   dabs build [recipe|path]
   ```

2. **Boot a fresh instance** — every `recipe --detach` is a NEW pristine DETACHED
   box (same recipe resolution as `build`); it brings the box up but does NOT run
   the recipe's command. Capture the instance name it prints:

   ```bash
   dabs recipe [recipe|path] --detach     # recipe booted: myproj (id: myproj-a3f9c21d4e02)
   ```

   The instance is named after the recipe's **image**, not the recipe. Recipes
   that share an image share a name prefix, so `dabs ls` cannot tell you which
   recipe made a box — unless you NAME it: `--name <n>` makes the boot's LEAF
   node (the box, or the place a boxless recipe provisions) carry your name as
   its id — shown everywhere ids are shown, resolvable everywhere ids resolve
   (`exec`, `rm`, `cd`, `--worktree`). Names are unique across known nodes; a
   name held by an INACTIVE node reaps that record on the fly, a name held by
   active work refuses. `dabs cd <node>` prints a node's WORKING place as a bare
   path, resolved per kind — a project to its source repo, a worktree to its
   checkout (`~/.dabs/nodes/<id>/held/worktree`), a box to its node dir
   (`~/.dabs/nodes/<id>`) — for `cd "$(dabs cd myfix)"`. A box's node dir holds
   its three spaces as subdirectories — `volume/` (survives `rm --keep`), `held/`
   (work you would miss: a worktree's checkout, a workdir's copy; `rm` asks
   first), `tmp/` (scratch, reaped quietly).

3. **Use it directly**, or **run an agent inside it — with a recipe.** Recipes
   do the plumbing: a recipe is a fully declarative box (image, what to
   mount/copy in, env, command). dabs ships FIVE generic recipes — `sh` (a
   shell in a clean box over the cwd), `wt` (cut a git worktree, no box),
   `wtbox` (a shell box over a fresh worktree), `scratch` (copy the cwd into a
   directory node, no box), and `scratchbox` (a shell box over a throwaway copy
   of the cwd); all work anywhere, `dabs recipes` lists them. Here is `sh`, the
   shape to copy when you write your own into `~/.dabs/recipes.yaml`:

   ```yaml
   recipes:
     sh:                                # ships out of the box → dabs recipe sh
       image: shell
       command: [sh]
       sources:
         - mount: .                     # your cwd, live — edits persist on the host
           path: /work
   ```

   Tool- or project-specific recipes are NOT bundled — they live in your
   `~/.dabs/recipes.yaml` (global) or a project's `./dabs.yaml`. A Claude Code
   box, for instance, mounts YOUR login dir, so it's yours to define, not dabs's
   to ship. This repo's own `dabs.yaml` defines `claude`, `fresh-claude`,
   `review`, and more — copy those as a starting point:

   ```yaml
   recipes:
     claude:
       image: claude
       command: [claude]
       env: { CLAUDE_CONFIG_DIR: /root/.claude }
       sources:
         - mkmount: ~/.dabs/shared/claude       # the login dir, shared by every box that names it
           path: /root/.claude
         - mkmount: $PARENT_VOLUME/claude/projects # this place's sessions; reload on the next box, survive `rm`
           path: /root/.claude/projects
         - mount: .                             # your cwd, live — edits persist on the host
           path: /work
   ```

   **Logging a harness in is just running the recipe.** `mkmount:` creates its
   host dir (0700) if it isn't there, so the first box boots with an empty login
   dir, Claude says "not logged in", you `/login` once inside, and every later box
   that mounts that dir is logged in. There is no separate login command.

   Recipes resolve **bundled → `~/.dabs/recipes.yaml` (global) →
   `./dabs.yaml` (project)**, later winning. A project's `dabs.yaml` can add
   recipes and set a `default:`; `dabs recipe` with no name runs that default (no
   default set → the bundled `sh` box). The same registry backs `dabs
   build`/`recipe`: a recipe carries the image, env, workdir, and target, so
   `build` resolves a recipe just like `recipe`.

   **Run a one-off command in a box — `dabs recipe -- <cmd…>`.** Three shapes:
   - `dabs recipe <name> [cmd…]` — a KNOWN recipe; any trailing tokens are
     appended to its command.
   - `dabs recipe -- <cmd…>` — the project `default:` recipe (the bundled `sh` box
     if there's no `dabs.yaml`/default) with everything after `--` appended. This
     is the replacement for the old `dabs do`.
   - `dabs recipe` (no args) — the default recipe with its OWN command.

   A first token that is neither `--` nor a known recipe is an ERROR listing the
   known recipes — a typo never silently becomes a command. Because you're handing
   a box an arbitrary command, dabs prints the recipe and the exact command and
   asks for a **y/N** confirmation before it builds or runs anything (the
   default-recipe path always confirms; a named recipe confirms only when you
   append a command).

   **`recipe` appends — it does not give you a shell.** What a trailing command
   yields depends entirely on the recipe's own `command`. Against the bundled `sh`
   box, `dabs recipe sh -c 'echo hi'` runs `sh -c 'echo hi'`. Against a recipe
   whose command is `claude -p '…'`, the same argv is appended to *Claude's*
   command line, which is almost never what you meant. Read `dabs.yaml`, then pick
   the recipe explicitly.

   **Sources — four kinds.** Each entry names its origin with exactly one of:

   | kind | what lands in the box | the host |
   |---|---|---|
   | `mount` | a live bind; the box's writes hit the host | must exist — a missing origin is a typo, and dabs refuses it |
   | `mkmount` | a live bind | created (0700) if absent — say it where you mean "provision this" |
   | `worktree` | a fresh git branch off HEAD, mounted live | your tree is untouched; reap with `dabs worktrees` |
   | `copy` | a snapshot taken at box start | untouched |

   **Nodes and their three spaces.** A node is a marker for a place dabs
   provisioned — kind `project | workdir | worktree | box`, chained
   `project → (workdir | worktree)? → box`. Every node has three directories, and
   the one a recipe mounts declares what happens to the bytes (`rm` reads the
   space, not the recipe). A source path may name the box node's spaces:

   ```
   $NODE_VOLUME      survives `rm --keep`  — this box's caches
   $NODE_HELD       `rm` asks first       — work you would miss  ($NODE_EPHEMERAL: alias)
   $NODE_TMP         `rm` reaps quietly    — scratch
   ```

   The `$PARENT_*` family names the same three spaces of the box's PARENT place
   (the project/workdir/worktree it stands on) instead of the box's own node.
   Use `$PARENT_VOLUME` for what a box wants back on the NEXT box: a fresh box
   is a fresh node with an empty `$NODE_VOLUME`, but its parent place persists,
   so sessions written to `$PARENT_VOLUME` reload next time. Both families
   substitute into source paths only; they are not environment variables inside
   the box. An `mkmount:` into `$PARENT_VOLUME` nested over a shared mount gives
   one box its own persistent slice of an otherwise shared tree — that is how the
   `claude` recipe keeps its sessions across re-ups and an `rm --keep`.

   **Recipes provision; skills prompt.** A recipe describes how the box is
   provisioned (image, sources, command) and must NOT bake agent instructions
   into its `command` — that's the caller's/skill's job. For a Claude recipe
   that needs a fixed brief (e.g. `review`, `dumb-user`), keep the prompt in a
   skill under `skills/<name>/SKILL.md`, **mount** that dir where Claude Code
   discovers project skills (`path: /work/.claude/skills/<name>`, `ro: true`),
   and make the `command` just `claude -p 'Use the <name> skill.'` (add `Skill`
   to `--allowedTools`). See `dabs.yaml`.

4. **Reap the worktrees an agent left** (recipes keep them):

   ```bash
   dabs worktrees               # list them; HAS WORK vs clean
   dabs worktrees diff <name>   # what the agent changed
   dabs rm <name>               # reap ONE (refuses unreviewed work unless --force)
   dabs rm --clean-worktrees    # sweep every worktree with no unreviewed work
   ```

5. **Reap boxes when done — `dabs rm` is the single reaper.** It stops the box
   AND removes its node and spaces. Stopping a live box, or losing data a space holds,
   needs consent: `-y`/`--yes` (or an interactive y/N). Without it, rm prints
   what it WOULD reap and exits nonzero — it never silently tears a box down.

   ```bash
   dabs rm <node> -y               # stop the box and remove its node+spaces
   dabs rm <node> --keep -y        # stop the box but KEEP its node record
   dabs rm <name> --multiple -y    # act on ALL matches (needed for >1; the count is shown first)
   dabs rm <name> --dry            # preview what would be reaped; remove nothing
   ```

   Flags: `-y`/`--yes` skips the consent prompt (stop a live box, reap a held
   space); `--keep` keeps the node record instead of removing; `--multiple` authorizes a
   prefix matching several nodes; `--volume` also reaps the volume; `--dry`
   previews; `--force` is ONLY for discarding a worktree's unreviewed git work —
   a different risk than the prompt `-y` skips, so it stays its own flag.
   `--clean-worktrees` takes no node name: it sweeps EVERY worktree that holds no
   unreviewed work in one shot (add `--force` to reap the ones that do). A
   worktree carrying a LIVE box is kept and named — stopping a machine needs the
   same `-y` a named `rm` asks for.

**Re-attaching to an existing worktree — `dabs recipe <recipe> --worktree
<wt>`.** A recipe's `worktree:`/`mount:`/`copy:` `.` source normally means "the
cwd". `--worktree` binds it to an EXISTING worktree instead (by name from `dabs
worktrees ls`): `worktree:`/`mount:` mount that worktree live — and also mount its
parent `.git`, so **git works inside the box** and the agent's commits reconcile
straight into the shared store (no push). It composes with `--detach`. Use it to
point a fresh agent (or a different recipe, e.g. review) at work another agent
already started, without cutting a new branch.

## Notes

- Tell the in-box agent the shape of its world: a fresh machine, no host
  access, whatever the Dockerfile installed. It only sees the box.
- One instance per agent: instances are cheap (`dabs recipe --detach` again) and isolated;
  sharing a box couples runs.
- Boxes are copies, not mounts — rebuild after editing source, and a box
  only contains what its Dockerfile installed.

## Facts you must respect

- Boxes are copies, not mounts: the image froze the program at the last
  `dabs build`. If you edited the program, rebuild before the next run —
  otherwise you run stale code.
- Writes inside a box persist for that instance's lifetime; pristine again
  means a NEW box, not reusing the old instance.
- Isolation is filesystem and process, NOT network: a box has open outbound
  network access and dabs has no `--no-network` switch yet. Do not rely on a box
  to contain code that should not reach the network — it can phone home.
- The box only contains what the Dockerfile installed. Slim base images
  lack tools like `ps`; if a journey needs one, it belongs in the
  Dockerfile, not worked around.
- Instance names accept unambiguous prefixes (git-style) everywhere:
  `dabs exec myproj-a3f -- ls`. Ambiguity is an error for exec; for rm
  it is refused too — a name matching more than one node reaps NOTHING and
  lists the matches, and you must pass `--multiple` to act on all of them. An
  empty/blank name matches nothing (never "all"). `-y`/`--yes` only skips the
  consent prompt; it does not authorize multi-match reaping — the count is shown
  first and `--multiple` is the scope opt-in.
- `dabs exec` is your direct peek into a box (inspection, setup, planting
  fixtures), and the `--` separator picks the mode: `dabs exec <instance> --
  <cmd…>` runs an EXACT argv (no shell), while `dabs exec <instance> <shell…>`
  (no `--`) runs a shell command line (wrapped in `sh -c`, so pipes/globs/`&&`
  work). The tier above it, `dabs recipe [name] <cmd…>`, appends to a recipe
  (see above).
- Mounts land parent-before-child whatever order the recipe declares them in:
  actions sort them by box-path depth, because bwrap binds in argv order (a
  parent listed after its child silently masks it) while apple/docker resolve
  nesting themselves. Declaration order is yours to choose.
- `dabs rm --keep` keeps a box's record: it stops the box and reaps its spaces
  (`tmp/` silently, `held/` only with consent when it holds files, `volume/`
  never) but LEAVES the node record. A worktree's checkout lives in its OWN
  node's held space, so keeping a box never touches it — `dabs rm <wt>` (or
  `dabs rm --clean-worktrees`) does, and it still refuses unreviewed work. A
  kept box whose spaces are empty becomes inactive and drops out of the default
  `dabs ls` (it is a record of history, shown by `dabs ls --inactive`;
  `dabs rm --inactive` sweeps all of them).
- `dabs recipe` or `dabs build` must be run from a directory OUTSIDE `~/.dabs`:
  provisioning from inside dabs's own storage is refused by design — it would
  mark the node store itself as a project. This includes test drivers: give a
  journey its own directory under your home, never a path under `~/.dabs`.
- Everything dabs owns is namespaced: it only ever sees or removes its own
  boxes.
- Keep the build context under your home directory. A context under
  `/private/tmp` (agent scratchpad) was empirically found to fail `dabs build`
  on macOS with `failed to compute cache key … not found` (2026-07-09); under
  home it worked.

## Recipe quick reference (dabs.yaml)

```yaml
default: myproj                    # what build/up/recipe run with no name
recipes:
  myproj:
    image: { dockerfile: Dockerfile, context: . }   # or a bare image name
    workdir: /work
    env: { KEY: value }
    target: <server>               # route to a registered server; omit for local
    sources:
      - mount: .                   # what lands in the box
        path: /work                #   kinds: mount | mkmount | worktree | copy
      - mkmount: $NODE_VOLUME/cache  # a box-private dir that survives `rm --keep`
        path: /root/.cache
```

`dabs build [recipe|path]` builds a recipe's image; `dabs recipe [recipe|path]
--detach` boots a detached box from it (no command). Both take no arg (the registry
`default:`), a recipe name, or a path to a `dabs.yaml` (or a dir holding one).
A recipe is the whole box spec — image, env, workdir, target, sources.

## Working on the codebase

If you are changing dabs itself, not just using it:

Deprecated glossary terms never appear in new code, output, docs, or comments —
check `GLOSSARY.md`'s tags.

**Build, test, verify**

```bash
gofmt -w . && go vet ./...
go build ./...            # keep BOTH green: darwin and `GOOS=linux go build ./...`
go test ./...             # unit tests are hermetic (fakes) — no sandboxes needed
./util/reinstall.sh       # rebuild + install to $GOBIN
```

A change that touches a driver is not proven by unit tests — drive the real
system. Vendor tools lie: Apple's `container` is not Docker-flag-compatible;
`exec -i` fails on non-TTY stdin; docker export drops resolv.conf. The Linux
(bwrap) driver is exercised over ssh on a real host.

**Test dabs WITH dabs — `dabs recipe dabseption`.** You do not need to install a
branch's dabs on your host to try it. The `dabseption` recipe builds `dabs` from
`/work` inside a privileged, bubblewrap-carrying box and KEEPS the box. That dabs
runs sandboxed in the box while you (the agent) stay outside on the host — then
reach in:

```bash
dabs recipe dabseption                   # → box kept: dabseption-482e37bd203c
dabs exec <instance> -- dabs recipes     # exercise its CLI, no host install
dabs exec <instance> 'dabs recipe sh --detach' # the dabs in the box boots its OWN box
```

**The box boots nested boxes.** Its image stages a ready-built `shell` rootfs, so
`dabs recipe sh --detach` and `dabs recipe sh` work inside with no builder. Only `dabs build` cannot
run in there — it shells out to `docker`, which the box does not carry — and
nothing needs it to.

**Two recipes, one Dockerfile; they differ in ONE thing — what lands at `/work`:**

| recipe | `/work` is | use it to |
|---|---|---|
| `dabseption` | the cwd, mounted live | test the code you have right now |
| `dabseptionwt` | a FRESH worktree off the current branch | test a branch without disturbing the cwd |

A Dockerfile-backed image is named after its RECIPE, so these build two image
tags from the one Dockerfile — `dabs build dabseption` does not also ready
`dabseptionwt` (the layer cache makes the second build cheap).

`dabs recipe dabseptionwt --worktree <wt>` binds an EXISTING worktree instead, and
also mounts its parent `.git` — so git works in-box. Plain `dabs recipe
dabseptionwt` cuts a new worktree but does NOT mount the parent `.git`, so git is
blind in-box; use `--worktree` when a test needs in-box git.

This covers CLI behaviour, recipe resolution, worktree/keep/rm logic, git
in-box, nested boots, and error paths — the fast inner loop for changing dabs.
The FULL e2e suite also runs in there: `dabs exec <box> 'cd /work && go build
-o /usr/local/bin/dabs . && go test -tags e2e ./test/e2e'` — the suite builds
its fixture image from the staged `shell` when its own is not staged. One
suite run per box: the suite assumes a pristine $HOME, and a kept box
accumulates state — boot a fresh box (`--worktree <wt>` rebinds the same
checkout) for each run. `./run_e2e.sh` remains the one-command form.

**How a box boots its own boxes.** Three things, all declared in the recipe and
its Dockerfile (`contrib/recipes/dabseption.Dockerfile`) — no host script, no
pre-staging step, nothing to remember:

1. **A privileged outer box** — `target: INTERNAL-docker-privileged-for-nested-sandboxing`,
   so the nested bwrap driver can create user namespaces and mount.
2. **Overlay-capable bubblewrap in the image** — built from source, non-setuid.
   The distro package will not do.
3. **An inner image staged by the Dockerfile** — `COPY --from=<stage> / <dest>/rootfs`
   IS the export: the builder flattens a stage into a plain rootfs, which is
   exactly what a dabs bwrap image is (a `rootfs/` dir plus an `image.json`
   holding env and workdir — a `printf`, since you authored the stage and know
   them). Nothing has to run `docker` inside the box.

The trap, if you write your own such box: **dabs's state must not sit on
overlayfs.** bwrap cannot stack an overlay on one, and `/root` in a docker box IS
overlayfs — leaving `$HOME` there fails with `bwrap: Can't make overlay mount …
Invalid argument`. The privileged target already runs the box with a non-overlay
volume at `/tmp`, so set `ENV HOME=/tmp/h`. Docker seeds that volume from the
image's own `/tmp`, which is what carries the staged image in. (Only the overlay
*upperdir* — `instances/` — truly needs the non-overlay filesystem; the image
rootfs may live on overlayfs.)

None of this involves worktrees. Nesting and worktrees are independent knobs.

**Layout**

```
main.go / driver*.go   composition root: build the drivers, wire deps
                       one per line, no nested New. driver_<os>.go is
                       build-tagged; OS code never compiles into a foreign
                       binary.
cli/                   argv → typed params. Pure parsers (one stdlib
                       FlagSet per command). Owns dispatch errors.
core/params/           leaf contract: params structs + Actions interface.
                       Litmus: if it can't become a .proto (logic, deps,
                       funcs), it doesn't belong here.
core/config/           ~/.dabs/config.json (servers/drivers) load + save.
core/recipe/           dabs.yaml recipe registry: parse + merge + defaults.
core/actions/          ALL policy: recipe resolution, instance-name
                       resolution across the drivers, --force/--dry, routing
                       by target, user-facing messages.
core/sandbox/          mechanical driver contract — exact names in, state
                       out. Zero vendor imports, zero logic.
core/sandbox/<kind>/   one driver per kind (apple, bwrap, server). Drivers
                       do no resolution, no policy, no messaging.
```

**Rules that keep it clean**

- `cli` and `core/actions` never import each other — they meet only in main.
  Drivers import only `core/sandbox`; nothing imports a driver except the
  build-tagged selection files.
- Drivers stay mechanical. New policy (resolution, force/dry, aggregation)
  goes in `core/actions`; a driver only ever takes exact names.
- New verb checklist: params struct + Actions method → action file →
  pure parser → command-table entry + runX → fake method in cli_test.go.
- Self-contained: no references to private projects, machines, usernames, or
  home paths anywhere (code, comments, tests, commit messages). Example
  names are neutral (`demo-0`, `myproj`).
- Comments describe the code AS IT IS in this commit. Never write about what
  the code used to be, what it no longer does, what you considered and
  rejected, or how it compares to the version before yours ("this does NOT
  reimplement X", "we no longer support Y", "unlike the old Z"). The reader has
  no access to the change that introduced the line, and a comment arguing that
  the change was correct is addressed to a reviewer who is already gone. State
  the constraint the code cannot show; say nothing else. That history belongs in
  the commit message.
- Commit messages say WHY, and for driver changes include what was run
  against the real system and what it printed.
- Function names must be verbs.

**Git**

- Never commit or push unless explicitly told to. Make and verify the
  changes; leave committing and pushing to the human.

**Cutting a release**

A release is a CHANGELOG cut plus a `v*` tag — pushing the tag drives
`.github/workflows/release.yml` to build the four binaries
(darwin/linux × amd64/arm64) and attach them to the tag's GitHub release,
which is what the install script downloads. No version is embedded in the Go
source. Release changes go through a PR like any other change — never straight
to `main`.

1. On a branch, move the `## [Unreleased]` block in `CHANGELOG.md` into a dated
   `## [X.Y.Z] - <date>` section, leave a fresh empty `## [Unreleased]`, and add
   the `[X.Y.Z]: …/compare/vPREV...vX.Y.Z` link at the bottom. Pick the version
   by semver (pre-1.0, breaking changes ride a minor bump).
2. `gofmt -l .`, `go build ./...` **and** `GOOS=linux go build ./...`,
   `go test ./...` — all green.
3. Commit, push the branch, open a PR, and let it merge to `main`.
4. **After the PR is merged**, tag the merge commit on `main` and push the tag:
   `git tag -a vX.Y.Z -m "dabs vX.Y.Z" && git push origin vX.Y.Z`. The tag —
   not the PR — is what triggers the release build.
5. **Once the release has published**, re-verify the manual against it: the docs
   quote screens the released binary prints, and the `scribe` box installs
   `releases/latest`. Follow the **`verify-manual`** skill
   (`skills/verify-manual/SKILL.md`) — drive the `walkthroughs/` suite in the
   box, re-bless any screens the release changed, update the matching docs, and
   open a docs PR. Skip only if the release changed nothing the CLI prints.
