# dabs — dumb agent boxes

Disposable sandboxes you can hand to an AI agent. Each box is a pristine
"fresh machine" built from a Dockerfile; agents get at most one way in — a
single curried shell tool — and can't touch your host.

Built for **dumb-user testing**: spawn naive LLM-powered users that try to
figure out your CLI on a machine with no config, no history, and no help,
then watch where they get stuck.

## Requirements

- macOS 26+ on Apple Silicon with Apple's `container` CLI
  (`brew install container && container system start`).
  Each box is a lightweight Linux micro-VM (sub-second boot).
- Other platforms: no local driver yet.

## Install

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

[MIT](LICENSE)
