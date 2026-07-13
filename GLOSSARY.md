# dabs glossary

The canonical vocabulary of dabs — one entry per term, what it means, and where
it is used in the code. The **Inconsistencies to resolve** section at the end
records where the *same* concept is currently named differently; that record is
the reason this file exists and is meant to drive a terminology cleanup.

This document changes no code and renames nothing. Every "used in" reference was
checked against the tree at the branch point.

---

## The box lifecycle

A run flows: **recipe → build → up / cast → box (instance) → run / exec / do →
down**. The nouns below name the thing that lifecycle produces.

### box
The disposable, host-isolated environment an agent or command runs in — a
pristine machine that sees only what its image installed, with no view of the
host. The primary user-facing noun in `AGENTS.md` and `README.md`.
*Used in:* `AGENTS.md` (throughout), `README.md:3` ("Each box is a pristine…"),
`cli/commands.go` (`recipe --detach` help: "boots a NEW detached box"),
`core/sandbox/sandbox.go:14,44` ("attached into a box", "one running box").

### instance
One concrete, running box, born pristine from an image and named
`<name>-<hex-id>`. Every `recipe --detach` yields a new instance; the id
disambiguates multiple boxes from the same recipe. The name you pass to
`exec`/`rm`.
*Used in:* `core/sandbox/sandbox.go:35,51-55` (Info, Driver.Up/Run/Down),
`cli/commands.go` (`exec`/`rm` help), `README.md`.

### sandbox
The environment abstraction as a whole — the isolation mechanism, and the term
for the box as seen by the driver/contract layer. Same object as *box*/*instance*,
named from the implementation's point of view.
*Used in:* `cli/commands.go:30` (`ls` help: "list what dabs owns, as a tree"),
`core/sandbox/sandbox.go:1-8,24,42` (package + `Spec`/`BuildSpec` docs),
`core/actions/ls.go:11` (`Ls` renders the node tree), `README.md:3,42,75,96`.

### environment
Informal synonym used in prose for what a box provides (a machine's worth of
tools/state). Not a CLI term and not a type; appears descriptively.
*Used in:* prose in `AGENTS.md`/`README.md` (e.g. remote "cloud environment"
descriptions); no command or struct is named `environment`.

### dab / dabs
The project and CLI itself (`dabs <command>`). No singular "a dab" object exists
in the vocabulary — *dabs* is only the tool name and command prefix.
*Used in:* `cli/cli.go:38` (`usage: dabs <command> [args]`), `AGENTS.md`,
`README.md`, module path `github.com/jjmerino/dabs`.

---

## Verbs (commands)

### build
Build a recipe's box **image** (once per Dockerfile change). Resolves a recipe
(no name → `dabs.yaml` default, a name → that recipe, a path → a `dabs.yaml`).
*Used in:* `cli/commands.go:19`, `core/actions/build.go:11-15`,
`core/sandbox/sandbox.go:37-42` (`Driver.Build`).

### recipe --detach
Boot a NEW pristine detached box from a recipe (no command); prints the instance
name. Every `recipe --detach` is a fresh box.
*Used in:* `cli/commands.go` (`recipe` help), `core/actions/up.go`,
`core/sandbox/sandbox.go:45` (`Driver.Up`), `README.md`.

### cast
Run a recipe onto an **existing** worktree (by name from `worktrees ls`) instead
of the cwd, mounting that worktree and its `.git` live so git works in the box.
Implemented as `Recipe` with a `Worktree` set.
*Used in:* `cli/commands.go:23,61-66` (`runCast`), `AGENTS.md` ("Re-attaching").

### run
Run a shell command line inside an instance — args joined into one `sh -c` line,
so pipes/globs/`&&` work. No `--` needed.
*Used in:* `cli/commands.go:28`, `core/sandbox/sandbox.go:47` (`Driver.Run`),
`README.md:63`.

### exec
Run an EXACT argv inside an instance (no shell). The low-level exact peek.
*Used in:* `cli/commands.go:27`, `core/sandbox/sandbox.go:49` (`Driver.Exec`).

### recipe -- <cmd…>
Run a one-off command in a throwaway box via the project **default** recipe
(else the bundled `sh` box), appending the command to that recipe's command.
Prompts y/N first.
*Used in:* `cli/commands.go` (`runRecipe`), `AGENTS.md`.

### recipe (verb) / recipes
`recipe <name> [cmd…]` runs a named recipe box (no name → default). `recipes`
lists the known recipes and what each mounts.
*Used in:* `cli/commands.go:21,24,41-53,90-101`, `core/actions/recipe.go`.

### rm
The single reaper: stop a box AND remove its node and spaces (cascading to
whatever stands on it). Stopping a live box or losing held data needs consent —
`-y`/`--yes`, or an interactive y/N; without it rm previews what it would take
and exits nonzero. `--keep` ARCHIVES instead: stop the box but keep its node as
the record of what ran and from where. `--multiple` acts on all matches (a name
matching several is otherwise refused; the count is shown first), `--volume`
also reaps the volume, `--dry` previews, `--force` discards a worktree's
unreviewed git work.
*Used in:* `cli/commands.go`, `core/actions/rm.go`, `core/actions/instance.go`,
`core/sandbox/sandbox.go` (`Driver.Down`).

### ls
List what dabs owns as a node tree, grouped by fleet member; `--all` also shows
archived nodes. An empty fleet member prints `(nothing running)` and a tree with
no live box prints under `no box`.
*Used in:* `cli/commands.go:30`, `core/actions/ls.go:11-13,40`,
`core/sandbox/sandbox.go:55` (`Driver.Ls`).

### servers
Manage registered remote servers: `servers [ls] | add <name> [host] | rm <name>`.
*Used in:* `cli/commands.go:31,151-182`, `core/config/config.go`.

### worktrees
Inspect/reap recipe-created git worktrees: `worktrees [ls | diff <name> |
rm <name> | prune] [--force]`.
*Used in:* `cli/commands.go:25,68-88`, `AGENTS.md` ("Reap the worktrees").

---

## Recipes and their pieces

### recipe
A fully declarative description of a box: image, workdir, command, env, sources,
target, keep. The unit `recipe`/`cast`/`build` all resolve. Resolution
order: bundled (`sh`) → `~/.dabs/recipes.yaml` (global) → `./dabs.yaml` (project),
later winning.
*Used in:* `core/recipe/recipe.go:28-40` (`Recipe`), `AGENTS.md`, `dabs.yaml`.

### registry / default
A recipes file: a top-level `recipes:` map plus an optional `default:` naming
the recipe `dabs recipe` runs when given no name.
*Used in:* `core/recipe/recipe.go:22-26` (`Registry`), `AGENTS.md`.

### image
The frozen box template a driver builds and boots from. Referenced by a bare
NAME (reuse `~/.dabs/images/<name>`, build from a bundled recipe if missing) or
an inline `{dockerfile, context}` build recipe.
*Used in:* `core/recipe/recipe.go:31,42-47` (`ImageRef`),
`core/sandbox/sandbox.go:29` (`BuildSpec`).

### command
What runs inside the box. A recipe's `command` must NOT bake in agent
instructions (that is the caller's / a skill's job).
*Used in:* `core/recipe/recipe.go:34`, `AGENTS.md` ("Recipes provision; skills
prompt").

### keep
Recipe flag: keep the box alive after the command finishes (default: delete it).
*Used in:* `core/recipe/recipe.go:38`, `core/actions/recipe.go:123,139`.

### target
Which fleet driver runs a recipe/box — `""` = local, or a server / driver-kind
name. Servers are one kind of target; future driver kinds (modal, daytona) will
be targets without being servers.
*Used in:* `core/recipe/recipe.go:37`, `core/actions/real.go:15,37`,
`core/config/config.go:15-16`, `dabs.yaml` (`target:`).

### workdir / env
The cwd (`/work` default) and environment variables inside the box.
*Used in:* `core/recipe/recipe.go:32,35`, `core/sandbox/sandbox.go:27-28`,
`README.md:97-98`.

---

## Source kinds

A recipe's `sources:` list places things into the box at a `path`. Exactly one
of the four kinds names each source's origin and picks HOW it lands.
*All defined in:* `core/recipe/recipe.go:67-121`, `core/actions/recipe.go`.

### mount
A live bind — the box's writes hit the host and persist past the box (a shared
login dir, the cwd). The host path must exist: a missing one is a typo, and dabs
refuses it. `ro: true` makes it read-only.
*Used in:* `core/recipe/recipe.go:69-72,87`, `core/sandbox/sandbox.go:14-21`
(`Mount`).

### mkmount
A live bind that CREATES its host origin (0700) if it is not there. Say it where
you mean "provision this": a login dir a harness will fill, a session dir that
starts empty.
*Used in:* `core/recipe/recipe.go:72-74,88`, `core/actions/recipe.go` (`buildBox`).

### worktree
A fresh git branch off HEAD of the named repo, mounted live — how an agent gets
an isolated, reconcilable checkout. The checkout lives in its own worktree node's
ephemeral space. Reaped via `dabs worktrees`.
*Used in:* `core/recipe/recipe.go:75,89`, `AGENTS.md`.

### copy
A snapshot taken at box-start time — the box owns it, the host is untouched.
*Used in:* `core/recipe/recipe.go:76,90`.

---

## Nodes and spaces

### node
A marker for one place dabs provisioned, recorded at `~/.dabs/nodes/<id>/`. It
carries a `kind`, a `parent`, and the recipe that made it — so listing, reaping
and casting read what dabs wrote rather than sniffing the filesystem.
*Used in:* `core/actions/node.go:20-73`.

### kind (of node)
What a node marks: **project** (the directory a command ran from — dabs records
it and never reaps it), **workdir** (a host directory a recipe mounted or copied
as `.`), **worktree** (a git worktree dabs cut), **box** (one running sandbox).
The chain is constrained to `project → (workdir | worktree)? → box`: boxes never
nest, a worktree is never cut inside a box.
*Used in:* `core/actions/node.go:52-65` (`NodeKind`).

### space
One of the three directories every node offers. Which one a recipe mounts
declares what happens to the bytes — convention, not configuration: `rm` reads
the space, not the recipe.

| space | `rm` |
|---|---|
| `volume/` | keeps it (unless `--volume`) |
| `ephemeral/` | asks before deleting a non-empty one |
| `tmp/` | removes it silently |

*Used in:* `core/actions/node.go`, `core/actions/rm.go` (`reapSpaces`).

### $NODE_VOLUME / $NODE_EPHEMERAL / $NODE_TMP
The box node's three spaces, supplied by dabs to source paths so a recipe can
name them without knowing an id. They are substituted in source paths ONLY — not
exported into the box's environment.
*Used in:* `core/actions/recipe.go` (`mintBoxNode`, `expandPathWith`),
`core/recipe/recipe.go:79-85`.

### $PARENT_VOLUME / $PARENT_EPHEMERAL / $PARENT_TMP
The same three spaces of the box's PARENT place (the project/workdir/worktree the
box stands on) rather than the box's own node. A fresh box mints a new box node
with an empty `$NODE_VOLUME`, but the parent place persists — so `$PARENT_VOLUME`
is where a box keeps what it wants back on the next box (the shipped `claude`
recipe stores sessions there so they reload). Substituted in source paths ONLY.
*Used in:* `core/actions/recipe.go` (`spaceVars`, `expandPathWith`), `dabs.yaml`
(`claude`/`claudewt`/`scratch` recipes).

---

## Drivers, fleet, servers

### driver
One sandboxing system implementing the `sandbox.Driver` contract (Apple
`container`, bwrap, ssh/server). Drivers are MECHANICAL: they take EXACT names
and expose state; all policy (name resolution, force/dry, aggregation) lives in
`core/actions`.
*Used in:* `core/sandbox/sandbox.go:1-12,33-57`, `core/sandbox/<kind>/`,
`README.md:108-110`.

### kind
A driver's identity string ("apple", "bwrap", "ssh", …), stamped on `Info.Driver`
and reachable without any instances.
*Used in:* `core/sandbox/sandbox.go:57` (`Driver.Kind`), `core/actions/ls.go:13`.

### fleet
The set of drivers dabs dispatches across — the local driver always exists, plus
any configured remote targets. Instance names resolve across the whole fleet;
`ls` aggregates over it.
*Used in:* `core/actions/real.go:13-18`, `core/config/config.go:30-32`,
`README.md:89`.

### server
A registered remote machine that has dabs installed, reached over ssh with
pubkey auth. One *kind* of target. Managed via `dabs servers`.
*Used in:* `core/config/config.go:15-16`, `cli/commands.go:31,151-182`,
`README.md:75-76`.

---

## Inconsistencies to resolve

The core problem: **one object — the disposable environment — has four names**,
and the CLI's own help strings disagree on which to use at each lifecycle stage.
A dumb-user usability run (naive Haiku agents driving dabs) confirmed the
confusion is concentrated at **boot / image / naming time**, not at reach-in or
teardown.

### 1. The box / instance / sandbox / environment cluster (the main one)

The verb that MAKES the thing, the verb that LISTS it, and the verb that KILLS it
each name it differently — inside a single `dabs --help` listing:

| Stage | Command | Noun used | Evidence |
|-------|---------|-----------|----------|
| make  | `recipe --detach` | **box** | `cli/commands.go` "boots a NEW detached box" |
| build image | `build` | **box image** / **sandbox** | `cli/commands.go:19` "build a recipe's box image"; `core/sandbox` calls it a sandbox image |
| list  | `ls`    | **sandboxes** | `cli/commands.go:30` "list sandboxes" |
| list (empty output) | `ls` | **instances** | `core/actions/ls.go:40` prints "(no instances)" |
| reach in | `exec` | **instance** | `cli/commands.go` "run a command inside a box" |
| kill  | `rm`    | **nodes** | `cli/commands.go` "stop a box and remove its node" |

So `dabs ls` is *itself* internally inconsistent: its help says "sandboxes" but
its empty-state output says "(no instances)".

*Recommended canonical noun (proposal for review, not decided):* **box** as the
user-facing noun everywhere in help text and command output — `AGENTS.md` and
`README.md` already lead with it, and the dumb-user run showed users think in
"boxes". Keep **instance** only where identity/naming matters (`<name>-<hex>`,
i.e. "a box's instance name"), and confine **sandbox** to the driver/contract
layer (`core/sandbox`, `sandbox.Driver`, `Spec`) where it names the abstraction,
not the user's object. Retire **environment** as a loose synonym. Concretely, the
minimal help-string fixes would be: `ls` help → "list boxes"; `ls` empty output →
"(no boxes)"; `exec`/`rm` help → "box" (retaining "instance name" where a
specific id is meant).

### 2. `ps` vs `ls` — the discovery gap

Naive users reached for `dabs ps` (the muscle-memory verb, cf. `docker ps`)
before discovering `dabs ls`. `ps` is an accepted alias for `ls` (see the alias
map in `cli/cli.go`), so the muscle-memory verb works; it is just absent from
`dabs --help`. `ls` is defined at `cli/commands.go:30`.

### 3. "box image" vs "sandbox image"

`build`'s help says "box image" (`cli/commands.go:19`) while the contract layer
speaks of the "sandbox" image and "sandbox identity" (`core/sandbox/sandbox.go:24,29`).
Same artifact, two names — folds into the resolution of cluster #1 (drivers keep
"sandbox", user-facing help says "box").

### What tested WELL (leave alone)

The reach-in and teardown vocabulary was used confidently and correctly by naive
users: `dabs exec <instance>` and `dabs rm <instance>`. The source kinds
(`mount`/`mkmount`/`worktree`/`copy`), `recipe`, and `cast` did not
surface as points of confusion. The confusion is specifically the lifecycle
noun, and specifically at boot/list/image time.
