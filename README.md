# dabs — dumb agent boxes

Disposable sandboxes you can hand to an AI agent. Each box is a pristine
"fresh machine" built from a Dockerfile; run a command — or a whole agent via
a recipe — inside it, and it can't touch your host. Every `up` is a new
instance; boxes run locally or on a remote server.

## Requirements

dabs drives platform tools you install yourself. It detects them and points you
at the install command; it never installs anything for you.

- **macOS** on Apple Silicon — Apple's `container` CLI. Each box is a Linux
  micro-VM.
  `brew install container && container system start`
- **Linux** — `bubblewrap` (enters boxes) + `docker` (builds images). Boxes are
  bwrap + overlayfs.
  `apt install bubblewrap` · docker: https://docs.docker.com/engine/install/
- **Remote servers** (any of the above, driven over ssh) — `ssh` with pubkey
  auth on your side; dabs installed on the server.
- No Windows driver.

## Install

Prebuilt binary — download from
[Releases](https://github.com/jjmerino/dabs/releases), then put it on your
PATH:

```bash
chmod +x dabs && mv dabs ~/.local/bin/   # or anywhere on PATH
```

Or build from source (needs Go 1.23+):

```bash
go install github.com/jjmerino/dabs@latest
```

## Quick start

Describe the box with two files in your project:

```yaml
# dabs.yaml — the recipes: a box is a name, an image, sources, a command
recipes:
  myproj:
    image: { dockerfile: Dockerfile, context: . }
    command: [sh]
    env: { MY_FLAG: "1" }
    sources:
      - mount: .           # your cwd, live — edits persist on the host
        path: /work
```

```dockerfile
# Dockerfile — what "fresh machine" means for your program
FROM ubuntu:24.04
WORKDIR /work
RUN apt-get update && apt-get install -y git
```

Then:

```bash
dabs build myproj                # build the box's image
dabs up myproj                   # → myproj-a3f9c21d4e02 up   (a NEW pristine box)
dabs ls                          # the tree: what dabs owns, and where it runs
dabs run myproj-a3f 'ls | wc -l'         # run a shell line inside (instance prefixes ok, git-style)
dabs exec myproj-a3f -- ./mycli --help   # exec an exact command inside (no shell)
dabs recipe myproj               # boot a box and run its command
dabs down myproj-a3f             # stop it
```

Every `up` creates a **new** instance with a random id — the image is the clean
state, so "give me a fresh machine" is instant. `down <name> --dry` shows what a
name matches; a name matching more than one instance is refused unless you pass
`--multiple`.

## Nodes

dabs marks every place it makes, so it can tell you what ran, from where, and
whether anything is still live. `dabs ls` is that tree.

```
local (apple, this machine)
  myproj              project   ~/code/myproj
  └─ myproj-18f9c901  workdir   ~/code/myproj
     └─ sh-a88626a1   box       myproj-a3f9c21d4e02 · running
```

A node is a **project** (the directory a command ran from), a **workdir** (a
directory a recipe took as its `.`), a **worktree** (a git branch dabs cut), or a
**box**. They stack: `project → (workdir | worktree)? → box`.

A recipe with no image makes a place and stops — `dabs recipe wt` cuts a worktree,
no box. Point a box at one later, or never.

Each node offers three directories, and the one a recipe mounts says what happens
to the bytes:

| space | on `down` / `rm` |
|---|---|
| `volume/` | kept, unless you ask for it by name (`rm -y --volume`) |
| `ephemeral/` | reaped with consent; without it, kept and its path printed |
| `tmp/` | reaped, always |

A source names them with `$NODE_*` (this box's) and `$PARENT_*` (its place's —
what you want back next time, since a box never returns):

```yaml
- mkmount: ~/.dabs/shared/claude          # shared by every box that mounts it
  path: /root/.claude
- mkmount: $PARENT_VOLUME/claude/projects # this place's sessions; survive `down`
  path: /root/.claude/projects
```

`dabs down` stops a box and archives its node. `dabs rm <node>` removes a node and
whatever stands on it (it brings boxes down first). `dabs ls --all` shows the
archive.

## Remote servers

A sandbox can live on another machine that has dabs installed (a Mac mini or
Linux box sitting around), reached over ssh with pubkey auth. Register it,
then point a manifest at it:

```bash
dabs servers add homelab            # host defaults to the name; or: add homelab user@10.0.0.5
dabs servers ls                     # name  strategy destination
#   local     apple this machine
#   homelab   ssh homelab
dabs servers rm homelab             # unregister (remote sandboxes untouched)
```

Route a recipe to a server with `target: homelab` (omit for local). `dabs
build`/`up` then run there; `dabs ls` aggregates the whole fleet with a target
column; `run`/`down` address any instance by name wherever it lives.

## Recipe fields (dabs.yaml)

A recipe is the whole box spec. Recipes resolve bundled (`sh`) →
`~/.dabs/recipes.yaml` (global) → `./dabs.yaml` (project), later winning; a
top-level `default:` names the recipe `build`/`up`/`recipe`/`do` use when given
no name.

| field | default | meaning |
|---|---|---|
| `image` | (required) | a bare image NAME to reuse, or `{dockerfile, context}` to build |
| `workdir` | `/work` | cwd inside the box |
| `command` | — | what runs in the box |
| `env` | — | environment inside the box |
| `sources` | — | what lands in the box, and how |
| `target` | local | which fleet driver runs it |
| `keep` | `false` | keep the box alive after the command finishes |

Paths given to `build`/`up` may be a `dabs.yaml` or a directory containing one.

### Sources

Each source names its origin with exactly one of four kinds, and a `path`
inside the box:

| kind | what lands in the box | the host |
|---|---|---|
| `mount` | a live bind; the box's writes hit the host | must exist (`ro: true` for read-only) |
| `mkmount` | a live bind | created (0700) if absent |
| `worktree` | a fresh git branch off HEAD, mounted live | your tree untouched; reap with `dabs worktrees` |
| `copy` | a snapshot taken at `up` | untouched |

Host paths may use `~` and `$VAR`. dabs also supplies the box's three node
spaces to source paths — `$NODE_VOLUME` (survives `down`), `$NODE_EPHEMERAL`
(`down` asks first), `$NODE_TMP` (`down` reaps quietly) — so a box can keep a
private, persistent slice of an otherwise shared tree:

```yaml
sources:
  - mkmount: ~/.dabs/shared/claude          # a login dir every box shares
    path: /root/.claude
  - mkmount: $NODE_VOLUME/claude/projects   # this box's sessions, kept across `down`
    path: /root/.claude/projects
```

An agent harness logs in by running the box: the first `mkmount` creates the dir
empty, you log in once inside, and every later box that mounts it is logged in.

## Design

- `cli` parses argv into typed params; `core/actions` owns all policy;
  `core/sandbox` is the mechanical driver contract (exact names in, state
  out); drivers live under `core/sandbox/<vendor>` and are build-tagged when
  OS-coupled.
- The image is the frozen fresh machine — rebuild it to change what a box
  carries. What crosses the boundary at runtime is exactly what the recipe's
  `sources` declare, and nothing else.

## License

[Apache 2.0](LICENSE)
