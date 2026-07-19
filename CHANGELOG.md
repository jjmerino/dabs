# Changelog

All notable changes to dabs are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[0.4.1]: https://github.com/jjmerino/dabs/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/jjmerino/dabs/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/jjmerino/dabs/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jjmerino/dabs/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jjmerino/dabs/releases/tag/v0.1.0
