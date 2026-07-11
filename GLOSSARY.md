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
`cli/commands.go:26` (`up` help: "start a NEW detached box"),
`core/sandbox/sandbox.go:14,44` ("attached into a box", "one running box").

### instance
One concrete, running box, born pristine from an image and named
`<name>-<hex-id>`. Every `up` yields a new instance; the id disambiguates
multiple boxes from the same recipe. The name you pass to `run`/`exec`/`down`.
*Used in:* `core/sandbox/sandbox.go:35,51-55` (Info, Driver.Up/Run/Down),
`cli/commands.go:27-29` (`exec`/`run`/`down` help), `README.md:6,63,68,89,96`.

### sandbox
The environment abstraction as a whole — the isolation mechanism, and the term
for the box as seen by the driver/contract layer. Same object as *box*/*instance*,
named from the implementation's point of view.
*Used in:* `cli/commands.go:30` (`ls` help: "list sandboxes"),
`core/sandbox/sandbox.go:1-8,24,42` (package + `Spec`/`BuildSpec` docs),
`core/actions/ls.go:11` ("Ls lists sandboxes"), `README.md:3,42,75,96`.

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

### up
Boot a NEW pristine detached box from a recipe (no command); prints the instance
name. Every `up` is a fresh box.
*Used in:* `cli/commands.go:26`, `core/actions/real.go`/`buildup_test.go`,
`core/sandbox/sandbox.go:45` (`Driver.Up`), `README.md:61,68`.

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

### do
Run a one-off command in a throwaway box via the project **default** recipe
(else the bundled `sh` box), appending the command to that recipe's command.
Prompts y/N first.
*Used in:* `cli/commands.go:22,55-59` (`runDo`), `AGENTS.md` ("dabs do").

### recipe (verb) / recipes
`recipe <name> [cmd…]` runs a named recipe box (no name → default). `recipes`
lists the known recipes and what each mounts.
*Used in:* `cli/commands.go:21,24,41-53,90-101`, `core/actions/recipe.go`.

### down
Stop and remove instances by name; `--force` downs all matches, `--dry` previews.
*Used in:* `cli/commands.go:29`, `core/actions/down.go:11-15`,
`core/sandbox/sandbox.go:53` (`Driver.Down`).

### ls
List existing boxes across the fleet, grouped by fleet member. Empty members
print `(no instances)`.
*Used in:* `cli/commands.go:30`, `core/actions/ls.go:11-13,40`,
`core/sandbox/sandbox.go:55` (`Driver.Ls`).

### auth
Log a harness into a persistent vault that future boxes mount (e.g.
`auth claude`).
*Used in:* `cli/commands.go:20,34-39`, `core/actions/auth.go`.

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
target, keep. The unit `up`/`recipe`/`do`/`cast`/`build` all resolve. Resolution
order: bundled (`sh`) → `~/.dabs/recipes.yaml` (global) → `./dabs.yaml` (project),
later winning.
*Used in:* `core/recipe/recipe.go:28-40` (`Recipe`), `AGENTS.md`, `dabs.yaml`.

### registry / default
A recipes file: a top-level `recipes:` map plus an optional `default:` naming
the recipe `dabs recipe`/`do` runs when given no name.
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
`core/config/config.go:15-16`, `dabs.json` (`"target"`).

### workdir / env
The cwd (`/work` default) and environment variables inside the box.
*Used in:* `core/recipe/recipe.go:32,35`, `core/sandbox/sandbox.go:27-28`,
`README.md:97-98`.

---

## Source kinds

A recipe's `sources:` list places things into the box at a `path`. Exactly one
of the four kinds names each source's origin and picks HOW it lands.
*All defined in:* `core/recipe/recipe.go:66-108`, `core/actions/recipe.go`.

### mount
A live bind — the box's writes hit the host and persist past the box (vault,
pairing, the cwd). `ro: true` makes it read-only.
*Used in:* `core/recipe/recipe.go:70,82`, `core/sandbox/sandbox.go:14-21`
(`Mount`).

### worktree
A fresh git branch off HEAD of the named repo, mounted live — how an agent gets
an isolated, reconcilable checkout. Reaped via `dabs worktrees`.
*Used in:* `core/recipe/recipe.go:71,80`, `AGENTS.md`.

### copy
A snapshot taken at `up` time — the box owns it, the host is untouched.
*Used in:* `core/recipe/recipe.go:72,81`.

### perbox
A fresh, empty, box-private host dir mounted live. Its value is a LABEL (not a
host path); the dir is allocated per box under `~/.dabs/boxes/<id>/<label>` and
starts empty — used to give one box a private slice nested over an otherwise
shared mount.
*Used in:* `core/recipe/recipe.go:73-79,84`,
`core/actions/recipe.go:236-244,598-602` (`perboxDir`).

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
| make  | `up`    | **box**       | `cli/commands.go:26` "start a NEW detached box" |
| build image | `build` | **box image** / **sandbox** | `cli/commands.go:19` "build a recipe's box image"; `core/sandbox` calls it a sandbox image |
| list  | `ls`    | **sandboxes** | `cli/commands.go:30` "list sandboxes" |
| list (empty output) | `ls` | **instances** | `core/actions/ls.go:40` prints "(no instances)" |
| reach in | `exec`/`run` | **instance** | `cli/commands.go:27-28` "inside an instance" |
| kill  | `down`  | **instances** | `cli/commands.go:29` "stop + remove instances by name" |

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
"(no boxes)"; `exec`/`run`/`down` help → "box" (retaining "instance name" where a
specific id is meant).

### 2. `ps` vs `ls` — the discovery gap

Naive users reached for `dabs ps` (the muscle-memory verb, cf. `docker ps`)
before discovering `dabs ls`. Not a naming *conflict* but a discoverability miss.
*Proposal for review:* accept `ps` as an alias for `ls` (no code change made
here). `ls` is defined at `cli/commands.go:30`.

### 3. "box image" vs "sandbox image"

`build`'s help says "box image" (`cli/commands.go:19`) while the contract layer
speaks of the "sandbox" image and "sandbox identity" (`core/sandbox/sandbox.go:24,29`).
Same artifact, two names — folds into the resolution of cluster #1 (drivers keep
"sandbox", user-facing help says "box").

### What tested WELL (leave alone)

The reach-in and teardown vocabulary was used confidently and correctly by naive
users: `dabs run <instance>` and `dabs down <instance>`. The source kinds
(`mount`/`worktree`/`copy`/`perbox`), `recipe`, `cast`, `up`, and `do` did not
surface as points of confusion. The confusion is specifically the lifecycle
noun, and specifically at boot/list/image time.
