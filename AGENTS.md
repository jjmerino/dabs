# AGENTS.md — running things in a dabs box

You are (presumably) a capable agent with host access. dabs lets you run a
command — or a whole agent — inside a disposable box that has no view of your
host. Reach in with `dabs run <instance> <shell…>` (or `dabs exec <instance> --
<cmd>` for an exact argv), or run a whole agent inside via a recipe
(`dabs recipe claude`, defined in this repo's `dabs.yaml`).

## Read `dabs.yaml` first

Before you run or test anything with dabs in this repo, **read `./dabs.yaml`**.
It decides what every bare command does. Nothing below is meaningful until you
know what is in it:

- **`default:`** is what `dabs build`, `dabs up`, `dabs recipe`, and `dabs do`
  resolve to when you pass no name. It is NOT a shell by default. In this repo
  it is `review` — a `claude -p` agent — so a bare `dabs do -c 'echo hi'`
  appends your argv to Claude's, boots an agent, and prints nothing for minutes.
  Want a shell? Name one: `dabs do` is not it; `dabs recipe sh -c 'echo hi'` is.
- **Which recipes exist**, and what each one mounts, copies, and runs. `dabs
  recipes` lists them; `dabs recipes --print` dumps the bundled ones.

## The loop

1. **Build the box image** (once per Dockerfile change) — `build` resolves a
   RECIPE (no name → the registry `default:`, a name → that recipe, a path → a
   `dabs.yaml` to load) and builds its image:

   ```bash
   dabs build [recipe|path]
   ```

2. **Boot a fresh instance** — every `up` is a NEW pristine DETACHED box (same
   recipe resolution as `build`); it brings the box up but does NOT run the
   recipe's command. Capture the instance name it prints:

   ```bash
   dabs up [recipe|path]     # → myproj-a3f9c21d4e02 up
   ```

   The instance is named after the recipe's **image**, not the recipe. Recipes
   that share an image share a name prefix: `claude`, `fresh-claude`, and
   `review` all boot a `claude-…` box, so `dabs ls` cannot tell you which recipe
   made one.

3. **Use it directly**, or **run an agent inside it — with a recipe.** Recipes
   do the plumbing: a recipe is a fully declarative box (image, what to
   mount/copy in, env, command). dabs ships exactly ONE recipe — `sh`, the
   generic clean-box example; `dabs recipes` lists what's available. Here it is,
   the shape to copy when you write your own into `~/.dabs/recipes.yaml`:

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
   box, for instance, mounts YOUR auth vault, so it's yours to define, not
   dabs's to ship. This repo's own `dabs.yaml` defines `claude`, `fresh-claude`,
   `review`, and more — copy those as a starting point:

   ```yaml
   recipes:
     claude:
       image: claude
       command: [claude]
       env: { CLAUDE_CONFIG_DIR: /root/.claude }
       sources:
         - mount: ~/.dabs/auth/claude   # your shared vault — dabs mounts it, never copies
           path: /root/.claude
         - mount: .                     # your cwd, live — edits persist on the host
           path: /work
   ```

   Recipes resolve **bundled (`sh`) → `~/.dabs/recipes.yaml` (global) →
   `./dabs.yaml` (project)**, later winning. A project's `dabs.yaml` can add
   recipes and set a `default:`; `dabs recipe` with no name runs that default (no
   default set → it errors and lists the choices, so an agent must pick). The
   same registry backs `dabs build`/`up`: a recipe carries the image, env,
   workdir, and target, so `build`/`up` resolve a recipe just like `recipe`/`do`.

   **Run a one-off command in a box — `dabs do <cmd…>`.** `dabs do` is the quick
   "just run this in a sandbox": it resolves the project `default:` recipe (or
   the bundled `sh` box if there's no `dabs.yaml`/default), APPENDS your command
   to that recipe's command, and runs it in a throwaway box. `dabs recipe <name>
   <cmd…>` does the same for a named recipe. Because you're handing a box an
   arbitrary command, dabs first prints the recipe and the exact command and asks
   for a **y/N** confirmation before it builds or runs anything.

   **`do` appends — it does not give you a shell.** What you get depends entirely
   on the default recipe's own `command`. Against the bundled `sh` box,
   `dabs do -c 'echo hi'` runs `sh -c 'echo hi'`. Against a recipe whose command
   is `claude -p '…'` — like this repo's `review` default — the same argv is
   appended to *Claude's* command line, which is almost never what you meant.
   Read `dabs.yaml`, then pick the recipe explicitly.

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
   dabs worktrees rm <name>     # or `prune`; refuses unreviewed work unless --force
   ```

5. **Reap boxes when done:**

   ```bash
   dabs down <instance>            # exactly one match required
   dabs down <name> --multiple     # act on ALL matches (needed for >1)
   dabs down <name> --dry          # preview what a name matches
   ```

**Re-attaching to an existing worktree — `dabs cast <recipe> <worktree>`.** A
recipe's `worktree:`/`mount:`/`copy:` `.` source normally means "the cwd". `cast`
binds it to an EXISTING worktree instead (by name from `dabs worktrees ls`):
`worktree:`/`mount:` mount that worktree live — and also mount its parent `.git`,
so **git works inside the box** and the agent's commits reconcile straight into
the shared store (no push). Use it to point a fresh agent (or a different recipe,
e.g. review) at work another agent already started, without cutting a new branch.

## Notes

- Tell the in-box agent the shape of its world: a fresh machine, no host
  access, whatever the Dockerfile installed. It only sees the box.
- One instance per agent: instances are cheap (`dabs up` again) and isolated;
  sharing a box couples runs.
- Boxes are copies, not mounts — rebuild after editing source, and a box
  only contains what its Dockerfile installed.

## Facts you must respect

- Boxes are copies, not mounts: the image froze the program at the last
  `dabs build`. If you edited the program, rebuild before the next run —
  otherwise you run stale code.
- Writes inside a box persist for that instance's lifetime; pristine again
  means a NEW `up`, not reusing the old instance.
- The box only contains what the Dockerfile installed. Slim base images
  lack tools like `ps`; if a journey needs one, it belongs in the
  Dockerfile, not worked around.
- Instance names accept unambiguous prefixes (git-style) everywhere:
  `dabs exec myproj-a3f -- ls`. Ambiguity is an error for exec/run; for down
  it is refused too — a name matching more than one instance downs NOTHING and
  lists the matches, and you must pass `--multiple` to act on all of them. An
  empty/blank name matches nothing (never "all"). `--force` only skips
  confirmation; it does not authorize multi-match reaping.
- Three levels reach into an existing box, low to high:
  `dabs exec <instance> -- <cmd…>` runs an EXACT argv (no shell); `dabs run
  <instance> <shell…>` runs a shell command line (wrapped in `sh -c`, so
  pipes/globs/`&&` work, no `--` needed); `dabs do <cmd…>` appends to a recipe
  (see below). exec/run are your direct peek into a box (inspection, setup,
  planting fixtures).
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
      - mount: .                   # what lands in the box (mount/copy/worktree)
        path: /work
```

`dabs build [recipe|path]` builds a recipe's image; `dabs up [recipe|path]`
boots a detached box from it (no command). Both take no arg (the registry
`default:`), a recipe name, or a path to a `dabs.yaml` (or a dir holding one).
A recipe is the whole box spec — image, env, workdir, target, sources.

## Working on the codebase

If you are changing dabs itself, not just using it:

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
dabs run  <instance> 'dabs up sh'        # the dabs in the box boots its OWN box
```

**The box boots nested boxes.** Its image stages a ready-built `shell` rootfs, so
`dabs up sh` and `dabs do` work inside with no builder. Only `dabs build` cannot
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

`dabs cast dabseptionwt <worktree>` binds an EXISTING worktree instead, and also
mounts its parent `.git` — so git works in-box. Plain `dabs recipe dabseptionwt`
cuts a new worktree but does NOT mount the parent `.git`, so git is blind in-box;
cast when a test needs in-box git.

This covers CLI behaviour, recipe resolution, cast/worktree/keep/down logic, git
in-box, nested boots, and error paths — the fast inner loop for changing dabs.
`./run_e2e.sh` remains the full suite.

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
main.go / driver*.go   composition root: build the driver fleet, wire deps
                       one per line, no nested New. driver_<os>.go is
                       build-tagged; OS code never compiles into a foreign
                       binary.
cli/                   argv → typed params. Pure parsers (one stdlib
                       FlagSet per command). Owns dispatch errors.
core/params/           leaf contract: params structs + Actions interface.
                       Litmus: if it can't become a .proto (logic, deps,
                       funcs), it doesn't belong here.
core/config/           ~/.dabs/config.json (servers/fleet) load + save.
core/recipe/           dabs.yaml recipe registry: parse + merge + defaults.
core/actions/          ALL policy: recipe resolution, instance-name
                       resolution across the fleet, --force/--dry, routing
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
- Commit messages say WHY, and for driver changes include what was run
  against the real system and what it printed.
- Function names must be verbs.

**Git**

- Never commit or push unless explicitly told to. Make and verify the
  changes; leave committing and pushing to the human.
