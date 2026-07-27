# Changelog

All notable changes to dabs are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

There is no `Unreleased` section: each release's entry is written when the
release is cut, from the commit log since the previous tag.

## [0.5.0] - 2026-07-27

### Added
- **`dabs info <node>`** — one node's full model in one screen: kind and id, the
  working place it marks, its three spaces with each one's presence, and the
  recipe that provisioned it. The recipe comes from a snapshot taken when the
  node was created, so `info` shows what actually built the box rather than
  whatever the registry says today; nodes made before this release fall back to
  the registry, said plainly. `info` is also where a box's on-disk location
  now lives, and where its boot command is shown (below).
- **A box's boot command is persisted on its node.** `dabs recipe <name>
  <extra…>` appended tokens to the recipe's command and recorded them nowhere,
  so the inventory could say a box's recipe, when, and where — never WHAT it was
  asked to do. The appended argv is now stored on the box node and shown by
  `dabs info` alone: an appended command can carry a prompt or a secret, so it
  is never surfaced in the fleet-wide `dabs ls`.
- **`$NODE_ID` in a recipe's box paths.** A mount's destination `path:` and the
  recipe's `workdir:` may now name the box's own id. A recipe that mounts
  `. -> /work` gives every box the same cwd, so every agent session inside
  derives the same transcript slug and they all collide; `path: /$NODE_ID`
  namespaces the in-box workdir per box. `$NODE_ID` is the only variable a box
  path may name (space vars still name host origins only) and is validated as a
  slug, so it cannot become a traversal escape.
- **`dabs recipe --detach`** — boot a box with the recipe's command RUNNING in
  the background. The call returns as soon as the command is started, nothing is
  wired to the terminal, and the box is left up with its command alive — so a
  recipe whose command never exits (a server, a multiplexer) can be launched and
  left running. The command's combined output — both streams, interleaved — goes
  to `detached.log` in the box node's own directory
  (`~/.dabs/nodes/<id>/tmp/detached.log`), which the boot prints a `tail -f` line
  for and which is reaped with the node. The box stays until `dabs rm` whether or
  not the command exits: `keep` decides the fate of a
  box dabs is WAITING on, and nothing waits on a detached one. A recipe that
  declares no command is refused, pointing at `--no-command`. Detaching needs a
  driver whose box carries a process of its own — the new optional
  `sandbox.Detacher` capability. dabs ASKS the driver (`CheckDetach`) before
  booting anything, the way it already asks about egress: the docker and apple
  drivers answer yes; the bwrap driver answers no and says why (it enters the box
  with a fresh bwrap per command, so a command cannot outlive the call that
  started it). The refusal carries the driver's own words rather than a cause the
  caller guessed.
- **`sandbox.Capable`** — one interface listing every optional driver capability,
  which every driver WRAPPER is pinned to. A caller reaches a capability by
  type-asserting the driver it holds, so a wrapper that forgets to forward one
  does not fail: it answers "this driver cannot", naming a driver that can.
  `Capable` makes that a compile error, and a test reads the package's own source
  so a newly declared capability missing from the list fails too.

### Changed
- **`dabs ls` is one flat local tree.** All local drivers collapse into a single
  heading-less tree, so a project hosting boxes on more than one local driver
  (apple + docker) renders once instead of duplicated per driver; remote servers
  keep their own section, and each box names its driver in KIND — `box (apple)`,
  `box (docker)`. The old `(location)` continuation row is gone: WHERE becomes a
  per-kind INFO column — a project's source repo, a worktree's checkout, a box's
  copy-pasteable `dabs exec <instance> bash` — with the git signal riding the
  STATE column, and empty space cells render blank rather than a dash.
- **`dabs cd` resolves per kind** — a project to its source repo, a worktree to
  its checkout, a box to its node dir, instead of one uniform path.
- **`--detach` now means what detach means everywhere else.** The flag that
  booted a box and deliberately did NOT run the recipe's command is now
  **`--no-command`** — the name its own success line already printed, the
  successor the glossary had named, and, since 0.4.1, an accepted alias of the
  old `--detach`, so a script already passing `--no-command` is unaffected by
  this release. `--detach` is not an alias of it; it is the
  true detach described above. Pre-1.0, the old spelling is gone rather than
  deprecated: a script passing `--detach` now STARTS the recipe's command instead
  of skipping it, and must move to `--no-command`.
- **The e2e suite runs hermetically.** The box the suite runs in carries
  `egress: none`, so no test can reach the internet — the suite is proven closed,
  not merely well-behaved. Every test fakes its upstream instead of dialing a
  public host: a terminal engine hook answers where a status or redirect is the
  point, and a loopback TLS/HTTP server the suite process runs stands in where a
  policy needs the tunnel to actually reach an upstream and carry its bytes back.
  There is no online subset — `run_e2e.sh` and CI run the one hermetic suite.
- **Booting a box from inside a worktree's own checkout now parents it on that
  worktree.** Provisioning from a cwd under `~/.dabs` was refused outright
  ("inside dabs's own storage"), on the assumption the cwd would be marked as a
  new project. Booting a box from a worktree's checkout
  (`~/.dabs/nodes/<id>/held/worktree`) is now the exception: dabs resolves the
  owning worktree and attaches the box to it — mounting the checkout and its
  parent `.git` so git works in-box — exactly as an explicit `--worktree` would.
  The attach applies only to worktree checkouts; a cwd inside a workdir or
  scratch node's place stays refused. Making a project, worktree, or scratch
  node from under `~/.dabs` stays refused, and the refusal message now names
  what it refuses.
- **The example Anthropic credential broker swaps tokens only in credential
  positions.** The contrib broker (never shipped with dabs; an example egress
  module) previously replaced its dummy sentinel anywhere in a request body, so
  a token string quoted in message content was also expanded. It now swaps only
  the `Authorization` header and the `refresh_token` field of the refresh
  grant, optionally restricted to named `hosts:`; a token string appearing in
  message content instead passes through unchanged (a real one is rewritten
  back to the dummy) and is recorded to an optional host-side `alerts:` file.
- **`dabseption` boxes drop you into bash.** The command tail was `exec sh`,
  inherited from the bundled recipes where `sh` is the safe
  lowest-common-denominator; these boxes run a fixed Debian image that ships
  bash, so the prompt had no history, completion, or line editing for no reason.
  `TERM` is set too, so the shell has a real terminal.

### Fixed
- **`rm --clean-worktrees --dry` previews only what the sweep would take.** The
  sweep decided a worktree's fate by running the reap and reading its error, and
  `--dry` never errors — so the preview announced the removal of worktrees the
  real sweep keeps, and omitted the "kept N worktree(s) with unreviewed work"
  summary. Dry and real now select the same set.
- **The look-before-run confirmation no longer hangs on a stdin nobody can
  answer.** `dabs recipe <name> <cmd…>` read a line from stdin whenever stdin
  was not a terminal; an agent harness or CI step passes an inherited pipe that
  is open but never written, so the scripted form of the command hung forever
  with the prompt on stderr. The wait is bounded when no terminal is attached:
  `echo y | dabs …` still approves, an empty stream gets the default deny.
- **A kept box is offered back by its node id**, not the generated instance
  handle — so a box booted with `--name api` is reaped as `dabs rm api`.
- **A resolved recipe survives a JSON round-trip.** An open (unset) `egress`
  marshalled to a bare `{}`, which its own unmarshaler rejects; since a node now
  persists its recipe snapshot, that made every newly-provisioned node's record
  unreadable and `dabs ls` silently dropped it.

### Security
- Weekly Dependabot updates for gomod and github-actions, a `SECURITY.md`
  routing reports through GitHub private vulnerability reporting, `contents:
  read` on the test workflow, and third-party actions pinned to full-length
  commit SHAs.

## [0.4.1] - 2026-07-18

### Fixed
- **Release binaries now embed the egress forwarder.** The release workflow
  built dabs without `-tags withforwarder`, so every installed binary refused
  proxy egress at boot ("built without an embedded forwarder"); only
  `egress: none` worked. Releases now rebuild `forward.bin` per target arch and
  embed it, matching `util/reinstall.sh`.

## [0.4.0] - 2026-07-17

### Changed
- **Visibility follows life, not history.** `dabs ls` now shows only ACTIVE
  subtrees — a project and everything under it, judged as a unit: active when any
  node in it has a running box or holds real files in a space, inactive otherwise.
  Empty project markers (minted on every boot) and gone-and-empty boxes no longer
  clutter the listing. This replaces the **archived** concept: the flag `--all`
  becomes `--inactive` and shows ONLY the inactive subtrees; a one-line hint under
  `ls` points to it.

### Added
- **`dabs rm --inactive`** — sweep every inactive subtree (the empty markers `ls`
  hides), any node kind, in one shot; `--dry` previews. Distinct from
  `--clean-worktrees`, which sweeps worktree nodes only.

### Fixed
- Bringing a box down (`rm --keep`) now takes the box node too when nothing is
  left in its spaces, so an empty box no longer lingers as a `gone` record; a box
  that left files behind keeps its record.
- The "holds files" test counts only real files — a tree of only empty
  directories reads as empty everywhere it is consulted (the `ls` space cells, the
  `rm` consent, and the new activity check share one predicate).

## [0.3.0] - 2026-07-13

The redesign release: one grammar, nodes with spaces, and a vocabulary that is
documented, deprecated in place, and enforced. Breaking — the 0.2.0 verbs
`up`/`down`/`do`/`run`/`images`/`cast` are gone; the table under **Changed**
maps each old form to its replacement.

### Added
- **Recipes are the whole box spec** — a fully declarative schema
  (`image`, `command`, `env`, `sources`, `keep`, `target`, `description`) in
  `dabs.yaml`, resolving bundled (`sh`) → `~/.dabs/recipes.yaml` → project,
  later winning. `dabs recipes` lists them.
- **Nodes** — a record for everything dabs provisions (`project | workdir |
  worktree | box`), chained into a tree that `dabs ls` renders live. The node
  id is the canonical handle every verb resolves (git-style prefixes work);
  driver instance names still resolve as a fallback.
- **Node spaces** — every node carries three directories with distinct reap
  rules: `volume/` (outlives the box; deleting it always takes `--volume`),
  `held/` (something outside points at it; `rm` asks first), `tmp/` (scratch,
  reaped silently). Recipes address them as `$NODE_*`/`$PARENT_*` source vars;
  `$PARENT_VOLUME` is what makes state (e.g. agent sessions) survive to the
  next box on the same place.
- **Worktree nodes** — a `worktree:` source cuts a fresh branch off HEAD into
  the node's held space and mounts it live; `dabs worktrees [ls | diff]`
  inspects them, `rm --clean-worktrees` sweeps every worktree holding no
  unreviewed work, and `recipe --worktree <wt>` binds an existing one with its
  parent `.git` so git works in-box.
- **`prune`** — reclaim built images; refuses to break a live box unless
  `--force`, `--dry` previews.
- **`GLOSSARY.md`** — the canonical vocabulary, one word one meaning, with
  deprecation tags naming each successor term.
- **A regression e2e suite** grown from live agent bug-hunts
  (`test/e2e/bugs_e2e_test.go`): every fixed bug is pinned by a test that
  replays the agent chain that found it.
- **Connect timeouts on every remote call** — ssh/scp to a registered server
  give up after 6s and name the unreachable host, instead of hanging forever.
- **Styled CLI** (lipgloss) with plain deterministic output when piped.

### Changed
- **One grammar: `recipe` is the only boot verb, `exec` the only runner.**

  | 0.2.0 | 0.3.0 |
  |---|---|
  | `up <recipe>` | `recipe <name> --detach` |
  | `run <box> <shell…>` | `exec <box> <shell…>` (no `--` → one `sh -c` line) |
  | `exec <box> -- <argv>` | unchanged (`--` → exact argv) |
  | `down <box>` | `rm <box> --keep` |
  | `do <cmd…>` | `recipe [--] <cmd…>` |
  | `images prune` | `prune` |
  | `cast <recipe> <wt>` | `recipe <name> --worktree <wt>` |

  Old forms error rather than silently meaning something new; an unknown
  recipe name lists the known ones. dabs's own flags end at the first bare
  `--`; everything after it reaches the box command verbatim.
- **The `ephemeral` space is now `held`.** Old nodes' `ephemeral/` dirs remain
  readable, and `$NODE_EPHEMERAL`/`$PARENT_EPHEMERAL` stay as permanent
  aliases — existing recipes keep working.
- **`rm` is the one teardown verb**: a single confirmation covers a whole
  cascade (with a preview that shows live boxes as live), `--keep` archives,
  and the four risks stay separately gated — `-y` (the loss), `--multiple`
  (the scope), `--force` (unreviewed git work), `--volume` (the volume).
- **`ls` and `rm` tell one story** — one tree, live states in previews, idle
  places under their machine's heading, and worktree states distinguish
  `unmerged` (commits ahead) from `has work` (dirty only).
- **One error voice across drivers** — a box command's own failure passes
  through bare; driver-machinery failures carry the vendor CLI's output.
  (Previously each driver had its own dialect, and docker's differed.)
- **Builds skip images that already exist**; local boxes resolve before
  remote ones.

### Deprecated
- In prose and new work (the CLI may still print them, the glossary tags each
  with its successor): **fleet** (say drivers), **gone** (future box statuses),
  **archived** (name pending), **`--detach`** (future `--no-command`),
  **consent** (say confirmation), the **`no place`**/**`boxes with no node`**
  headings (future: orphaned).

### Removed
- **The verbs `up`, `down`, `do`, `run`, `images`, `cast`, and
  `worktrees rm|prune`** — see the grammar table above.
- **The `dabs.json` manifest** — a recipe in `dabs.yaml` is the only box spec.
- **The `dabash` MCP tool and its harness integrations** — the `dabs mcp`
  command, the `core/mcpserve` server, the `dabs install`/`dabs uninstall`
  commands, and the bundled `harnesses/` integrations (a Claude skill, a pi
  extension). Unused — the mcp/dabash + harness integrations were the pre-box
  way to drive a dabs box; agents now run inside the box via recipes. If you
  were relying on it, please file an issue to bring it back.

### Fixed
- Concurrent `recipe --detach` in one directory minted duplicate project
  nodes; resolve-or-create is atomic now (the node dir is the lock).
- A single-node `rm` of a live box acted without confirmation; `prune`
  reclaimed the image a live box was running on.
- Relative `dabs.yaml` paths (`recipe .`, `recipe ./dabs.yaml`) resolve; a
  bare name colliding with a same-named directory is no longer read as a path.
- `exec -- <cmd>` errors with usage instead of hunting for a box named `--`;
  `--help` renders single-character flags with one dash.
- Ghost workdir nodes, per-node confirmation spam on cascades, multi-match
  teardown without `--multiple`, and glyphs breaking piped output.

## [0.2.0] - 2026-07-06

### Added
- **`docker` sandbox driver** — run boxes as plain docker containers, selectable
  from a manifest with `"target": "docker"`. Unprivileged by default.
- **`INTERNAL-docker-privileged-for-nested-sandboxing` driver** — the docker
  driver's privileged variant (`--privileged` + a non-overlay `/tmp` volume), for
  running a *nested* dabs sandbox (bwrap) inside a docker box. Internal/opt-in.
- **`dabs install [pi|claude]` and `dabs uninstall <harness>`** — install or
  remove the dabash harness integrations (a Claude skill, a pi extension). The
  integration files are embedded in the binary (`//go:embed`), so install works
  from a downloaded release, not only a source checkout.
- **`DABS_NAME` in every box** — dabs now sets `DABS_NAME=<instance>` in the box
  environment across drivers, so a program can detect it is sandboxed (the dabash
  guard keys on this).
- **Driver-agnostic e2e CLI test suite** (`test/e2e`, behind `//go:build e2e`)
  and `run_e2e.sh`, which drive the real `dabs` CLI inside a dabs box.

### Changed
- **The bwrap driver no longer requires docker to run prebuilt images.** docker
  is now checked only in `Build` (image building); `up`/`run`/`down`/`ls` need
  only `bwrap`. A host that only runs prebuilt images needs no docker.

## [0.1.0] - 2026-07-02

Initial release. Minimum to bootstrap dabs.

### Added
- Core CLI: `build`, `up`, `run`, `down`, `ls`, `mcp`, `servers`.
- Drivers: `apple` (Apple `container` micro-VMs, macOS), `bwrap`
  (bubblewrap + overlay, Linux), and `ssh` servers.
- `dabs.json` manifest (`name`, `workdir`, `env`, `dockerfile`, `context`,
  `target`) + Dockerfile-based images.
- `dabash` MCP tool, curried to a single instance via `dabs mcp <instance>`.

[0.5.0]: https://github.com/jjmerino/dabs/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/jjmerino/dabs/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/jjmerino/dabs/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/jjmerino/dabs/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jjmerino/dabs/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jjmerino/dabs/releases/tag/v0.1.0
