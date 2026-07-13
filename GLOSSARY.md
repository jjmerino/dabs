# dabs glossary

The canonical vocabulary of dabs — one word, one meaning. Where a concept once
had several names, the cluster has been resolved to a single term; the
**Resolved history** section at the end records those decisions so a reader who
learned an old name can find the new one.

References point at the function or type that owns a concept, not a line number,
so they age with the code rather than drifting from it.

---

## Vocabulary at a glance

| word | meaning | where you meet it |
|---|---|---|
| **box** | the disposable, host-isolated environment a command or agent runs in | the user-facing noun in `AGENTS.md`/`README.md`; `recipe --detach` boots one |
| **instance** | the driver's name for one running box, `<name>-<hex>` | `dabs ls` INSTANCE-in-parens, `Driver.Up/Run/Down` |
| **node** | the record dabs wrote for one thing it provisioned; its `id` is the canonical handle | `~/.dabs/nodes/<id>/`, `actions.Node` |
| **place** | a node a box can stand on — a project, workdir, or worktree (everything that is not a box) | `provisionPlaces`, the ls chain |
| **space** | one of a node's three directories — `volume`/`held`/`tmp` — that decides what `rm` does with the bytes | `SpaceVolume`/`SpaceHeld`/`SpaceTmp`, `reapSpaces` |
| **recipe** | a fully declarative box (image, sources, env, command, target, keep) | `recipe.Recipe`, `dabs.yaml` |
| **image** | the frozen template a driver builds and boots a box from | `recipe.ImageRef`, `Driver.Build` |
| **reap** | to remove a node and the spaces it holds (what `dabs rm` does) | `Rm`, `reapSpaces` |
| **consent** | the explicit permission a losing action needs — four flags for four risks | `-y` / `--multiple` / `--force` / `--volume` |
| **archived** | a box whose node record is kept but whose instance is gone (`rm --keep`) | `archive`, `dabs ls --all` |
| **live / gone** | a box's STATE: its driver holds it, or it does not | `CellLive`/`CellGone` |
| **no-diff / has work / unmerged** | a worktree's STATE: clean · uncommitted-or-untracked · commits ahead | `worktreeState` |
| **target** | which fleet member runs a box — `""` (local) or a named server/driver | `recipe.Recipe.Target`, `driverFor` |
| **server** | a registered remote machine with dabs installed, reached over ssh | `dabs servers`, `config` |
| **driver** | one sandboxing mechanism behind the `sandbox.Driver` contract | `core/sandbox/<kind>` |
| **fleet** | the whole set of drivers dabs dispatches across | `Real.drivers`, `dabs ls` |
| **worktree** | a fresh git branch off HEAD, cut into a node's held space and mounted live | the `worktree:` source, `dabs worktrees` |
| **detach** | boot a box and leave it up without running the recipe's command | `recipe --detach`, `upDetached` |

---

## The box, and the record of it

### box
The disposable, host-isolated environment a command or agent runs in — a
pristine machine that sees only what its image installed, with no view of the
host. The primary user-facing noun.
*Where:* `AGENTS.md`/`README.md` throughout; `recipe --detach` boots one;
`sandbox.Spec` describes one to a driver.

### instance
The driver's name for one concrete, running box, minted after boot and shaped
`<name>-<hex>`. It is distinct from the node id (which is minted first, before
the box exists), so a box has two names and its node record links them. `ls`
shows the instance in parentheses beside the box's node id; `exec`/`rm` resolve
either.
*Where:* `sandbox.Info`, `Driver.Up/Run/Down`, `viewNode` (box WHERE cell).

### node
The record dabs wrote for one thing it provisioned, at
`~/.dabs/nodes/<id>/dabs-node.json`. It carries a `kind`, a `parent`, and the
recipe that made it, so listing and reaping read what dabs wrote rather than
sniffing the filesystem.
*Where:* `actions.Node`, `writeNode`/`readNode`/`listNodes`.

### handle / node id
A node's `id` — the canonical, stable handle. `rm`/`exec`/`recipe --worktree`
all resolve it first, git-style: an exact id wins, then a unique prefix. A raw
box instance name resolves only as a fallback, for a box no node claims.
*Where:* `mintNodeID`, `rmMatches`, `matches`.

### place
A node a box stands ON — a project, workdir, or worktree; everything that is not
a box. A box mounts what a place owns, and a place is re-entered by every later
box, which is why what a box wants back next time belongs in the place's volume,
not the box's own. The chain is `project → (workdir | worktree)? → box`.
*Where:* `provisionPlaces`, the ls section chain.

### kind (of node)
What a node marks: **project** (the directory a command ran from — dabs records
it and never reaps its `Dir`), **workdir** (a host directory a recipe copied as
`.`), **worktree** (a git worktree dabs cut), **box** (one running sandbox).
*Where:* `NodeKind` (`KindProject`/`KindWorkdir`/`KindWorktree`/`KindBox`).

### archived
A box reaped with `--keep`: its instance is stopped but its node record stays, so
what ran and from where outlives the box. Its spaces are already gone. Archived
boxes are hidden by default and shown by `dabs ls --all`.
*Where:* `archive`, the `ls` archived count, `dabs ls --all`.

### live / gone
A box node's STATE cell: **live** when a driver in the fleet holds its instance,
**gone** when none does (it is archived, or its instance died).
*Where:* `CellLive`/`CellGone`, `viewNode`.

### sandbox
The isolation abstraction as the driver/contract layer sees it — the same object
a user calls a *box*, named from the implementation's point of view. Confined to
`core/sandbox` (`sandbox.Driver`, `Spec`, `BuildSpec`); it is not a user-facing
noun.
*Where:* `core/sandbox`.

---

## Verbs

### build
Build a recipe's box **image** (once per Dockerfile change). Resolves a recipe
(no name → the `dabs.yaml` default, a name → that recipe, a path → a `dabs.yaml`
to load).
*Where:* `Build`, `Driver.Build`.

### recipe
`recipe [name] [cmd…]` boots a box from a named recipe and runs its command,
then tears the box down (unless the recipe says `keep`). With no name — or a
leading `--` — it runs the DEFAULT recipe (the `dabs.yaml` `default:`, else the
bundled `sh` box) with the command appended, always confirming first.
*Where:* `Recipe`/`runRecipe`.

### recipe --detach
Boot a NEW pristine box from a recipe and leave it up WITHOUT running the
recipe's command. It leads its output with the box's node id (the canonical
handle) and prints the driver instance on its own line; the box is yours to reach
with `exec` and to reap with `rm`. A boxless recipe (no image) detaches cleanly —
it provisions its places and stops.
*Where:* `upDetached`, `printUp`.

### recipe --worktree \<wt>
Bind an EXISTING dabs worktree (by name from `worktrees ls`) to the recipe's `.`
source instead of the cwd, mounting the worktree and its parent `.git` live so
git resolves inside the box. Replaces the deleted `cast` verb. Composes with
`--detach`.
*Where:* `bindWorktree`, `Recipe`/`upDetached`.

### exec
Run a command inside a box — the single reach-in verb. `exec <box> -- <argv>`
runs an EXACT argv with no shell; `exec <box> <tokens…>` (no `--`) joins the
tokens into one `sh -c` line so pipes/globs/`&&` work. Replaces the deleted `run`
verb (that shell-line behavior is now `exec` without `--`).
*Where:* `Exec`, `Driver.Run`.

### ls
List what dabs owns as a node tree, grouped by fleet member; `--all` also shows
archived boxes. A place with no live box lists under its machine's heading, not a
separate bucket. An empty fleet member prints `(nothing running)`.
*Where:* `Ls`, `renderForest`.

### rm
The single reaper: stop a box AND remove its node and the spaces it holds,
cascading to whatever stands on it. Losing anything needs consent (see
**consent**); without it, `rm` previews what it would take and exits nonzero — it
never silently tears a box down. `--keep` archives instead of removing.
`--clean-worktrees` takes no node name: it sweeps EVERY worktree that holds no
unreviewed work in one shot (`--force` reaps the ones that do), previewing with
`--dry`.
*Where:* `Rm`, `rmCleanWorktrees`, `reapSpaces`, `Driver.Down`.

### worktrees
Inspect the worktree nodes recipes provision: `worktrees ls` lists them (STATE in
the three-value vocabulary, DETAIL carrying branch/recipe/box-liveness) and
`worktrees diff <name>` shows a review diff that surfaces untracked files. There
are only these two subcommands — reaping is `dabs rm <name>` or
`dabs rm --clean-worktrees`.
*Where:* `Worktrees`.

### recipes
List the known recipes and what each mounts; `--print` dumps the bundled recipes
YAML (the authoring format) to copy into `~/.dabs/recipes.yaml`.
*Where:* `Recipes`.

### prune
Reclaim built box images (they rebuild on the next build). `--dry` lists what
exists; `--force` removes even an image a live box depends on.
*Where:* `Prune`.

### servers
Manage registered remote servers: `servers [ls] | add <name> [host] | rm <name>`.
*Where:* `ServersList`/`ServersAdd`/`ServersRemove`, `config`.

---

## Recipes and their pieces

### recipe
A fully declarative description of a box: image, workdir, command, env, sources,
target, keep. Resolution order: bundled (`sh`) → `~/.dabs/recipes.yaml` (global)
→ `./dabs.yaml` (project), later winning.
*Where:* `recipe.Recipe`, `loadRegistry`.

### registry / default
A recipes file: a top-level `recipes:` map plus an optional `default:` naming the
recipe `dabs recipe` runs when given no name.
*Where:* `recipe.Registry`.

### image
The frozen box template a driver builds and boots from. Referenced by a bare NAME
(reuse `~/.dabs/images/<name>`, build from a bundled recipe if missing) or an
inline `{dockerfile, context}` build recipe.
*Where:* `recipe.ImageRef`, `Driver.Build`.

### command
What runs inside the box. A recipe's `command` must not bake in agent
instructions — that is the caller's or a skill's job.
*Where:* `recipe.Recipe.Command`.

### description
A recipe's one-line human summary, shown in `dabs recipes`.
*Where:* `recipe.Recipe.Description`.

### keep
Recipe flag: keep the box alive after the command finishes (default: delete it).
A kept box is yours to reap with `dabs rm`.
*Where:* `recipe.Recipe.Keep`.

### target / server / driver / fleet
**target** is which fleet member runs a box — `""` (local) or a named
server/driver kind. A **server** is one kind of target: a remote machine with
dabs installed, reached over ssh. A **driver** is one sandboxing mechanism behind
the `sandbox.Driver` contract (`apple`, `bwrap`, `docker`, `ssh`), plus the
reserved `INTERNAL-docker-privileged-for-nested-sandboxing` kind used only when a
box must itself run a nested sandbox. The **fleet** is the whole set of drivers
dabs dispatches across; the local driver always exists, and instance names
resolve across the whole fleet.
*Where:* `recipe.Recipe.Target`, `driverFor`, `Driver.Kind`, `Real.drivers`.

### workdir / env
The cwd (`/work` default) and environment variables inside the box.
*Where:* `recipe.Recipe.Workdir`/`Env`, `sandbox.Spec`.

### at
Where a provisioning source (a `worktree:` or `copy:`) puts its bytes in the NEW
node's own spaces — e.g. `$NODE_HELD/worktree`. It lets the recipe say where the
checkout lands and what `rm` will do to it, rather than dabs deciding in secret.
Unset, it defaults to the node's held space.
*Where:* `recipe.Source.At`, `placeAt`.

---

## Source kinds

A recipe's `sources:` list places things into the box at a `path`. Exactly one of
the four kinds names each source's origin and picks HOW it lands.
*All defined in:* `recipe.Source`, `buildBox`.

### mount
A live bind — the box's writes hit the host and persist past the box. The host
path must exist: a missing one is a typo, and dabs refuses it. `ro: true` makes
it read-only.

### mkmount
A live bind that CREATES its host origin (0700) if it is not there. Say it where
you mean "provision this": a login dir a harness will fill, a session dir that
starts empty.

### worktree
A fresh git branch off HEAD of the named repo, cut into its own worktree node's
held space and mounted live — how an agent gets an isolated, reconcilable
checkout. Inspect with `dabs worktrees`; reap with `dabs rm`.

### copy
A snapshot taken at box-start time — the box owns it, the host is untouched.

---

## Nodes and spaces

### space
One of the three directories every node offers. Which one a recipe mounts
declares what happens to the bytes — convention, not configuration: `rm` reads
the space, not the recipe. Each answers one question:

| space | the question | on `rm` |
|---|---|---|
| `volume/` | *does this outlive the box?* | kept; deleting it always takes its own `--volume`, never bundled into `-y` |
| `held/` | *does something outside this box point at it?* | asks before deleting a non-empty one — deleting it breaks someone else |
| `tmp/` | *does anybody but the box care?* | removed silently |

**held** leads with the pointer, not the consequence: a git worktree record,
review tooling, or dabs's own workflows point INTO a held space, so removing it
breaks something outside the box — which is *why* `rm` asks first. A worktree's
checkout lives here.

**tmp** carries a promise: dabs never READS tmp's contents to decide anything.
`ls` may draw a display-only ⚠ on a tmp that holds files, but no reap, guard, or
consent ever branches on what is in tmp — it is the box's scratch and nobody
else's business.
*Where:* `SpaceVolume`/`SpaceHeld`/`SpaceTmp`, `reapSpaces`, `heldCell`.

### $NODE_VOLUME / $NODE_HELD / $NODE_TMP
The box node's three spaces, supplied by dabs to source paths so a recipe can
name them without knowing an id. Substituted in source paths ONLY — never
exported into the box's environment. **`$NODE_EPHEMERAL` is a permanent alias for
`$NODE_HELD`** (the held space's former name), so a recipe written before the
rename keeps provisioning into the same held space and is never broken.
*Where:* `spaceVars`, `mintBoxNode`, `expandPathWith`.

### $PARENT_VOLUME / $PARENT_HELD / $PARENT_TMP
The same three spaces of the box's PARENT place rather than the box's own node. A
fresh box mints a new box node with an empty `$NODE_VOLUME`, but the parent place
persists — so `$PARENT_VOLUME` is where a box keeps what it wants back on the next
box (the shipped `claude` recipe stores sessions there so they reload).
`$PARENT_EPHEMERAL` aliases `$PARENT_HELD` just as `$NODE_EPHEMERAL` does.
*Where:* `spaceVars`, `expandPathWith`, `dabs.yaml`.

---

## Reading `dabs ls`

### the columns
`ls` draws NODE · KIND · VOL · HELD · TMP · STATE · WHERE. VOL/HELD/TMP are the
three space cells; STATE is the box or worktree state; WHERE is where the bytes
live on disk (for a box, its driver instance in parentheses beside its node dir).
*Where:* `lsColumns`, `columnTitle`, `renderForest`.

### the space legend (✓ / ⚠)
Under any tree with a space column: **✓** the space is present and holds nothing
(safe to reap); **⚠** the space holds files a reap would lose. On `tmp`, the ⚠ is
display-only (see the tmp promise).
*Where:* `Cell.Symbol`, `styleCell`, the `hasSpaceColumn` legend.

### `no place` (heading)
Where an archived box whose place record is gone lists — its place is gone, so it
has nowhere to nest and lists flat. (Not to be confused with a worktree's
DETAIL cell reading `no box`, which means "no live box on this worktree".)
*Where:* the `no place` heading in `Ls`.

### `boxes with no node` (heading)
Where a box a driver holds but no node claims lists — booted by an older dabs or
by hand. Still yours, so still shown and still reapable by its instance name.
*Where:* the `boxes with no node` heading in `Ls`.

### the worktree states
A worktree's STATE — the same three values in `dabs ls` and `dabs worktrees ls`:
**no-diff** (clean), **has work** (uncommitted or untracked changes, nothing
ahead), **unmerged** (commits ahead of the base branch). Only commits ahead read
as unmerged; local-only work reads as work.
*Where:* `worktreeState`, `Worktrees`.

### unreviewed work
The uncommitted changes or unpushed commits a worktree holds that no space rule
can see. `rm` refuses to discard it without `--force`; `ls` flags a section
holding it. Review it with `dabs worktrees diff <name>`.
*Where:* `guardWorktreeWork`, `worktreeWork`.

---

## Consent

Four flags guard four different risks; they are never collapsed into one:

| flag | consents to |
|---|---|
| `-y` / `--yes` | stop a live box, and reap a held space that holds files |
| `--volume` | additionally delete the volume — what a place keeps on purpose |
| `--force` | discard a worktree's unreviewed git work |
| `--multiple` | act on more than one node a prefix matched |

`--multiple` is not `--all`: `--multiple` authorizes acting on the several nodes
ONE name matched, while `--clean-worktrees` (the sweep) acts on every worktree.
Without the matching flag, the losing action is refused and previewed first.
*Where:* `Rm`, `reapSpaces`, `guardWorktreeWork`, `rmMatches`.

---

## Other terms

### reap / keep / kept
To **reap** is to remove a node and the spaces it holds. **`--keep`** archives
instead (stop the box, keep the record). A space `rm` declines to reap (a held
space without `-y`, a volume without `--volume`) is reported **kept**, with its
path, so nothing is lost silently.
*Where:* `Rm`, `reapSpaces`, `archive`.

### boxless recipe
A recipe with no image: it provisions its places (a worktree, a copied directory)
and stops. There is no box to mount, so `--detach` and a plain `recipe` reach the
same outcome.
*Where:* `provisionNodes`.

### DABS_NAME
An environment variable dabs sets inside every box to the box's instance name, so
a program can detect it is running inside a dabs box.
*Where:* each driver's `Up`.

### --help-full-for-agents
A flag that prints the full agent-facing guide (recipes, examples) instead of the
short usage — the entry point an agent reads before driving dabs.
*Where:* `cli`.

### CLI aliases
Names people actually type resolve to the canonical verb: `ps`/`list` → `ls`,
`remove`/`delete` → `rm`, `worktree` → `worktrees`.
*Where:* the alias map in `cli`.

---

## Resolved history

This file once tracked an unresolved terminology tangle; the decisions below are
now canon. The record is kept so a reader who learned an older name lands here.

### the box / instance / sandbox cluster
One object — the disposable environment — had drifted across four names. Resolved:
**box** is the user-facing noun everywhere in help and output; **instance** is
kept only where the driver's `<name>-<hex>` identity matters; **sandbox** is
confined to the driver/contract layer (`core/sandbox`), where it names the
abstraction, not the user's object; **environment** is retired as a loose synonym.

### node = the record, not the box
A box is what the user runs; the **node** is the record dabs keeps of it (and of
every place). `rm`'s help speaks of removing a box's node because the node is what
persists — most of all for an archived box, where the record is all that is left.

### the ephemeral → held rename
The space once named **ephemeral** is now **held**, because the word that matters
is *why* dabs hesitates to delete it: something outside the box points at it.
Existing nodes keep their `ephemeral/` dirs (read via a legacy fallback) and
`$NODE_EPHEMERAL`/`$PARENT_EPHEMERAL` stay as permanent aliases, so no user's
data or recipe breaks. Separately, the view-model concept "a space holds bytes"
is called **holds** (`CellHolds`, the ⚠ "holds files") so it never reads as the
held space.

### deleted verbs
**cast** became `recipe --worktree` (bind an existing worktree). **run** became
`exec` without `--` (the shell-line form); `Driver.Run` survives at the contract
layer as the mechanism `exec` calls. The old lifecycle verbs `up`/`down` are
gone: booting is `recipe --detach`, reaping is `rm`.
