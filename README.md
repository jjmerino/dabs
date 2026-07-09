# dabs — dumb agent boxes

Disposable sandboxes you can hand to an AI agent. Each box is a pristine
"fresh machine" built from a Dockerfile; the agent reaches it through a
single curried shell tool (dabash) and can't touch your host. Every `up` is
a new instance; boxes run locally or on a remote server.

## Requirements

dabs is a single static binary with no runtime dependencies of its own. It
drives platform tools you install yourself — it detects them and points you
at the install command, but never installs anything for you.

- **macOS** 26+ on Apple Silicon — Apple's `container` CLI. Each box is a
  lightweight Linux micro-VM (sub-second boot).
  `brew install container && container system start`
- **Linux** — `bubblewrap` (enters boxes) + `docker` (builds images). Boxes
  are bwrap + overlayfs; millisecond starts.
  `apt install bubblewrap` · docker: https://docs.docker.com/engine/install/
- **Remote servers** (any of the above, driven over ssh) — `ssh` with pubkey
  auth on your side; dabs installed on the server.
- No Windows driver yet.

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

Describe the sandbox with two files in your project:

```json
// dabs.json — the manifest: identity + runtime contract
{ "name": "myproj", "env": { "MY_FLAG": "1" } }
```

```dockerfile
# Dockerfile — what "fresh machine" means for your program
FROM ubuntu:24.04
WORKDIR /work
COPY . /work
RUN ./install.sh
```

Then:

```bash
dabs build ./myproj              # build the box's image
dabs up ./myproj                 # → myproj-a3f9c21d4e02 up   (a NEW pristine box)
dabs ls                          # name  status  driver
dabs run myproj-a3f -- ./mycli --help    # exec inside (instance prefixes ok, git-style)
dabs down myproj-a3f             # remove it
```

Every `up` creates a **new** instance with a random id — the image is the
clean state, so "give me a fresh machine" is instant and there is nothing to
clean up. `down <name> --dry` shows what a name matches; `--force` downs all
matches.

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

Route a project to a server with `"target": "homelab"` in its `dabs.json`
(omit for local). `dabs build`/`up` then run there; `dabs ls` aggregates the
whole fleet with a target column; `run`/`down`/`mcp` address any instance by
name wherever it lives.

## Hand a box to an agent (dabash)

`dabs mcp <instance>` serves an MCP stdio server exposing exactly one tool,
**dabash(command, cwd?)** — dumb agent bash. The instance is bound at launch
(curried into argv): the tool has no sandbox parameter, so the agent cannot
address any other box.

```bash
dabs up ./myproj                 # → myproj-a3f9c21d4e02 up
claude --setting-sources "" --tools "" --strict-mcp-config \
  --mcp-config '{"mcpServers":{"dabash":{"command":"dabs","args":["mcp","myproj-a3f"]}}}' \
  --allowedTools "mcp__dabash__dabash" \
  -p "figure out what this machine's CLI does and try it"
```

That agent wakes up with no host filesystem, no user config, and one
capability: a shell inside its box.

## Manifest fields (dabs.json)

| field | default | meaning |
|---|---|---|
| `name` | (required) | sandbox identity; instances are `<name>-<hex>` |
| `workdir` | `/work` | cwd inside the box |
| `env` | — | environment inside the box |
| `dockerfile` | `Dockerfile` | build recipe, relative to the manifest |
| `context` | `.` | build context, relative to the manifest |

Paths given to `build`/`up` may be the manifest file or a directory
containing `dabs.json`.

## Design

- `cli` parses argv into typed params; `core/actions` owns all policy;
  `core/sandbox` is the mechanical driver contract (exact names in, state
  out); drivers live under `core/sandbox/<vendor>` and are build-tagged when
  OS-coupled. Zero dependencies.
- Boxes are copies, not mounts: source enters via image layers at `build`
  time. Editing your working tree never leaks into a running box.

## License

[Apache 2.0](LICENSE)
