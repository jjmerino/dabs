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

- **`default:`** is what `dabs build`, `dabs recipe --no-command`, and `dabs recipe`
  resolve to when you pass no name — and, for `recipe`, also when the first token
  is not a known recipe (then ALL tokens are appended to the default's command).
  It is NOT a shell. A `default:` naming a `claude -p` agent turns a bare
  `dabs recipe -c 'echo hi'` into your argv appended to Claude's — an agent that
  boots and prints nothing for minutes. This repo sets no `default:`, so
  `build`/`recipe --no-command` with no name list the choices, and `recipe` with an
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

2. **Boot a fresh instance** — every `recipe --no-command` is a NEW pristine
   box (same recipe resolution as `build`); it brings the box up but does NOT run
   the recipe's command. Capture the instance name it prints:

   ```bash
   dabs recipe [recipe|path] --no-command     # recipe booted: myproj (id: myproj-a3f9c21d4e02)
   ```

   `--detach` boots the same box but STARTS the recipe's command inside it in the
   background and returns without waiting — for a recipe whose command never
   exits (a server, a multiplexer). The command's combined output goes to
   `~/.dabs/nodes/<id>/tmp/detached.log` on the host (the boot prints the
   `tail -f` line), and is reaped with the node. It needs a driver whose box
   carries a process of its own — dabs asks the driver, so the refusal names the
   driver and its own reason. docker and apple can; bwrap cannot (it enters the
   box with a fresh bwrap per command), and there `--no-command` + `dabs exec` is
   the way.

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

   **Host sockets — `sockets:`, a key of its own.** A box may also be handed unix
   sockets a host program is already listening on. That is NOT a source kind: a
   socket provisions nothing, owns no node space, and `rm` never reads it, so it
   is its own top-level list — each entry a `socket:` (the host path, expanded
   like a source origin: `~`, `$VAR`, and the node space vars `$NODE_*`/
   `$PARENT_*`) landing at an absolute `path:` in the box:

   ```yaml
   recipes:
     myproj:
       image: shell
       sockets:
         - socket: /var/run/docker.sock # must already exist and BE a socket
           path: /run/dabs/docker.sock  # where the box finds it
   ```

   A socket crosses as filesystem, not network, so the box gets it under every
   egress mode, `none` included. The box `path:` obeys the same rules a source's
   does — absolute, no `..`, and `$NODE_ID` (the box's own id) is the only
   variable that resolves in it. Everything else about a socket refuses by name
   rather than booting a box that quietly reaches nothing: a `socket:` that is
   missing or that is not a socket, a `path:` landing on something dabs binds
   itself (`/run/dabs/door.sock`, `/run/dabs/egress.sock`, `/run/dabs/forward`,
   `/run/dabs/pub`, `/run/dabs/log`) or on a path another source or socket
   already claims, a `:` in either path, a recipe with no image (a place has no
   box to reach out of), and a `target:` naming a SERVER (the listener is on
   THIS host; a box on another machine has no path to it — a local target such
   as `docker` is fine).

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
straight into the shared store (no push). It composes with `--no-command` and
`--detach`. Use it to
point a fresh agent (or a different recipe, e.g. review) at work another agent
already started, without cutting a new branch.

## Services — reaching a box's port from the host

**Publishing is granted, not ambient.** A recipe that says `publish: true` boots
a box with a **door**: one dabs-owned unix socket, at `/run/dabs/door.sock` in
the box, answered on the host by a **relay** dabs starts before the box and
reaps with its node. A box without the grant has no door, and `forward publish`
in it refuses by name, saying what the recipe must set — a denied request reads
as a denial, not as a broken box.

In a granted box, a program publishes a box-local port under a name by running
the in-box forwarder:

```bash
forward publish <name> --type webui|general --port <n>   # inside the box
```

That dials the door and holds one **crossing** open for as long as it runs:
running it IS the registration, and the crossing closing IS the deregistration.
The HOST side writes the registry — `<name>.sock` and `<name>.json` in the box
node's `tmp/services` — so what `dabs services` lists is a socket this machine
opened, and it disappears when the box stops answering. A name is `[a-z0-9._-]`,
starting with a letter or digit, at most 64 bytes (it is a filename and a cell
the host prints, and the box choosing it is the untrusted side); anything else
is refused, and a name the host cannot print as written is not listed at all.
On the host:

```bash
dabs services              # NAME TYPE BOX INSTANCE HOST STATE (up | down | conflict)
dabs services serve        # forward each one from a stable 127.0.0.1 port; index on 127.0.0.1:28080
```

**One way in, the same on every driver.** The host dials the relay's socket; the
relay asks the box for a stream over the held crossing; the box opens it as
another dial out of the door and couples it to the published port. Nothing is
reached over the box's network, so a service is exactly as reachable under
`egress: none` or a proxy egress as under `open` — the door is filesystem, not
network — and bwrap, apple and docker all carry it.

**Each call is its own connection.** The first line of every crossing says what
it is (`DABS-DOOR/1 PUBLISH <name> <type> <port>`, `DABS-DOOR/1 STREAM <id>`);
past that line the relay reads nothing and decides nothing. A held crossing
carries a heartbeat, and both sides read on deadlines: an open connection proves
nothing about the peer, so liveness is an answer that had to be produced.

**What a door refuses.** The box is the untrusted side, so a door carries at
most 32 published services at once and holds at most 64 crossings that have not
yet said what they are; over either, the crossing is answered `BUSY` and the
publisher comes back — a busy moment must never cost a box its ability to
publish. A crossing that has said what it is stops counting against the second
limit, so a web UI holding connections open cannot crowd out the next publish.
`ERR` is only for a decision (a name already published in that box, an unknown
type), and the box gives up on those. One door is also one relay: a second
`dabs services relay` aimed at a live box's door is refused by name rather than
quietly taking its crossings.

A name keeps its port (42000–42999, persisted in `~/.dabs/service-ports.json`),
so n worktrees of one web project each get their own address and never fight
over a host port. `webui` only makes the index render a link; routing is raw TCP
either way, so websockets work. One name is one port: a second box claiming a
live name is reported as a conflict and is not served — name them per worktree.

## Notes

- Tell the in-box agent the shape of its world: a fresh machine, no host
  access, whatever the Dockerfile installed. It only sees the box.
- One instance per agent: instances are cheap (`dabs recipe --no-command` again) and isolated;
  sharing a box couples runs.
- Boxes are copies, not mounts — rebuild after editing source, and a box
  only contains what its Dockerfile installed.

## Facts you must respect

- Boxes are copies, not mounts: the image froze the program at the last
  `dabs build`. If you edited the program, rebuild before the next run —
  otherwise you run stale code.
- Writes inside a box persist for that instance's lifetime; pristine again
  means a NEW box, not reusing the old instance.
- A box has a network namespace of its own, and what that namespace reaches is
  the recipe's `egress:`. Under the default `egress: open` the box reaches
  anything your host can reach OUTWARD, but nothing of the host's own: a service
  on the host's loopback is out of reach, and a port the box binds is the box's
  alone — five boxes may bind `:3000` at once, and none of them takes the host's
  `:3000`. An open box can still phone home, so do not rely on `open` to contain
  code that must not reach the internet — that is `egress: none` or a proxy
  egress. On the bwrap driver `egress: open` is built by **pasta** (the `passt`
  package) and needs it installed and dabs running as an unprivileged user;
  without either, a boot refuses and says so rather than handing the box the
  host's network. pasta must be snapshot `2025_05_03` or newer, for the address
  flags dabs passes: Debian trixie+ and Fedora 41+ package a new enough one,
  Ubuntu's and Alpine's current packages refuse those flags, and there you build
  from https://passt.top at the version `contrib/recipes/dabseption.Dockerfile`
  pins.
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
- `dabs recipe` refuses to make a project, worktree, or scratch node from inside
  `~/.dabs`: marking the node store itself as a project is nonsense. The one
  exception is a dabs WORKTREE's checkout (`~/.dabs/nodes/<id>/held/worktree`) —
  a boot from in there parents the box on that worktree and mounts its parent
  `.git`, exactly as `--worktree` would, so working inside a checkout is
  supported. `dabs build` provisions no node at all, and runs from anywhere.
  Test drivers still get their own directory under your home: a journey wants a
  HOME whose dabs state is its own.
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
    sockets:                         # host unix sockets the box may talk to
      - socket: /var/run/docker.sock #   must exist and BE a socket; any egress
        path: /run/dabs/docker.sock  #   absolute box path
    publish: true                    # the box may publish services (default: it may not)
```

`dabs build [recipe|path]` builds a recipe's image; `dabs recipe [recipe|path]
--no-command` boots a box from it and runs no command. Both take no arg (the registry
`default:`), a recipe name, or a path to a `dabs.yaml` (or a dir holding one).
A recipe is the whole box spec — image, env, workdir, target, sources, sockets.

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
dabs exec <instance> 'dabs recipe sh --no-command' # the dabs in the box boots its OWN box
```

**The box boots nested boxes.** Its image stages a ready-built `shell` rootfs, so
`dabs recipe sh --no-command` and `dabs recipe sh` work inside with no builder. Only `dabs build` cannot
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

`dabs recipe dabseptionwt --worktree <wt>` binds an EXISTING worktree instead of
cutting one. Either way the box also gets the repo's `.git` at its own host
path, so git works in-box on a worktree dabs just cut and on one an earlier run
left behind alike.

This covers CLI behaviour, recipe resolution, worktree/keep/rm logic, git
in-box, nested boots, and error paths — the fast inner loop for changing dabs,
alongside `go test ./...`.

**The FULL `-tags e2e` suite runs in its own box — `./run_e2e.sh`.** The suite
needs more than dabseption carries: `bun` for the proxy engine, `openssl` to
mint its CA, and a dabs built `-tags withforwarder` so proxy egress has a
forwarder to mount. That is the `test/e2e/box` recipe, whose Dockerfile builds
FROM the dabseption image and adds exactly those. `./run_e2e.sh` is the whole
procedure: build `dabseption`, build `test/e2e/box`, boot it (`egress: none`,
so the run is hermetic), and `go test -tags e2e -v ./test/e2e` inside. Running
the suite in a plain dabseption box instead fails every proxy test with
`bun must be on PATH`.

One suite run per box: the suite assumes a pristine `$HOME`, and a box
accumulates state — each run gets a fresh box.

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
4. **pasta in the image, and an unprivileged user to drive it** — a nested box
   with the default `egress: open` gets its network namespace from pasta, and
   pasta serves only an unprivileged caller. The image carries pasta (built from
   source, like bubblewrap) and a `boxer` user with its own dabs state, so a
   nested open-egress box is booted as:

   ```bash
   dabs exec <instance> "su boxer -c 'HOME=/tmp/boxer dabs recipe sh --no-command --name b1'"
   ```

   As root the boot refuses and says why. A nested box with `egress: none` or a
   proxy egress needs none of this and boots as root.

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
- New OPTIONAL driver capability checklist: declare the interface in
  `core/sandbox` with "OPTIONAL driver capability" in its doc → add it to
  `sandbox.Capable` → forward it from every wrapper (`Lazy`). The compiler and
  `core/sandbox/capability_test.go` enforce the last two; the marker in the doc
  is what the test finds it by. A capability a wrapper drops does not fail — it
  answers "this driver cannot", naming a driver that can.
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
(darwin/linux × amd64/arm64) and attach them, `SHA256SUMS`, and `install.sh` to
the tag's GitHub release — the binaries are what the install script downloads.
No version is embedded in the Go source. `install.sh` is an asset of the
release because it names that release's binaries and verifies them against that
release's `SHA256SUMS`; dabs.dev serves
`releases/latest/download/install.sh`, and must never be repointed at the copy
on `main`, which can describe assets no published release carries. Release changes go through a PR like any other change — never straight
to `main`.

1. On a branch, WRITE the release's section in `CHANGELOG.md` at cut time: read
   `git log vPREV..` and each PR it names, and turn everything user-visible into
   a dated `## [X.Y.Z] - <date>` section under the Keep-a-Changelog categories.
   There is no `## [Unreleased]` section — a change lands without touching the
   changelog, and the cut is where the record gets written. Add the
   `[X.Y.Z]: …/compare/vPREV...vX.Y.Z` link at the bottom. Pick the version by
   semver (pre-1.0, breaking changes ride a minor bump).
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
