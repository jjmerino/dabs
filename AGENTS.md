# AGENTS.md — running things in a dabs box

You are (presumably) a capable agent with host access. dabs lets you run a
command — or a whole sub-agent — inside a disposable box that has no view of
your host. The box exposes exactly one way in: a shell tool, `dabash`.

## The loop

1. **Build the box image** (once per Dockerfile change):

   ```bash
   dabs build <dir-with-dabs.json>
   ```

2. **Boot a fresh instance** — every `up` is a NEW pristine box; capture the
   instance name it prints:

   ```bash
   dabs up <dir>          # → myproj-a3f9c21d4e02 up
   ```

3. **Use it directly**, or **hand it to a sub-agent.** `dabs mcp <instance>`
   is an MCP stdio server exposing ONE tool, `dabash(command, cwd?)`. The
   instance is bound at launch — the tool has no sandbox parameter, so the
   sub-agent cannot reach any other box or your host. Launch it with no
   builtin tools and no user config:

   ```bash
   claude --setting-sources "" --tools "" --strict-mcp-config \
     --mcp-config '{"mcpServers":{"dabash":{"command":"dabs","args":["mcp","<instance>"]}}}' \
     --allowedTools "mcp__dabash__dabash" \
     -p "<task>"
   ```

   Flag notes, learned the hard way: `--setting-sources ""` drops user
   config but KEEPS credentials (`--bare` breaks auth). `--allowedTools` is
   required in `-p` mode or the run stalls on a permission prompt. Pass the
   FULL instance name to `dabs mcp` (not a prefix) — an exact name resolves
   locally with no ssh probe, so the server starts instantly.

4. **Reap when done:**

   ```bash
   dabs down <instance>            # or: dabs down <name> --force  (all instances)
   dabs down <name> --dry          # preview what a name matches
   ```

## Notes

- Tell the sub-agent the shape of its world: "You have one tool, dabash,
  which runs shell commands inside your machine. There is no other
  filesystem or host."
- One instance per sub-agent: instances are cheap (`dabs up` again) and
  isolated; sharing a box couples runs.
- Boxes are copies, not mounts — rebuild after editing source, and a box
  only contains what its Dockerfile installed.

## Facts you must respect

- Boxes are copies, not mounts: the image froze the program at the last
  `dabs build`. If you edited the program, rebuild before the next run —
  otherwise the dumb agent tests stale code.
- Writes inside a box persist for that instance's lifetime; pristine again
  means a NEW `up`, not reusing the old instance.
- The box only contains what the Dockerfile installed. Slim base images
  lack tools like `ps`; if a journey needs one, it belongs in the
  Dockerfile, not worked around.
- Instance names accept unambiguous prefixes (git-style) everywhere:
  `dabs run myproj-a3f -- ls`. Ambiguity is an error for run/mcp and an
  informational list for down.
- `dabs run <instance> -- <cmd…>` is your own direct peek into a box
  (inspection, setup, planting fixtures). Shell syntax needs `sh -c '…'`.
- Everything dabs owns is namespaced: it only ever sees or removes its own
  boxes.

## Manifest quick reference (dabs.json)

```json
{ "name": "myproj", "workdir": "/work", "env": {"KEY": "value"},
  "dockerfile": "Dockerfile", "context": "." }
```

`name` is required; the rest default sensibly. Paths resolve relative to
the manifest's directory. `dabs build`/`up` accept the manifest file or a
directory containing `dabs.json`. `"target": "<server>"` routes the sandbox
to a registered server (see `dabs servers`); omit for local.

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

**Dependencies.** dabs itself has ZERO third-party Go deps and no cgo, so it
is a static binary that cross-compiles to every target with a plain
`GOOS=… GOARCH=… go build` (keep it that way). What it needs are external
tools AT RUNTIME, per driver — Apple `container` (macOS); `bwrap` + `docker`
(Linux); `ssh`/`scp` (servers). dabs never installs these: each driver's
`New()` checks for its tools and returns an error with the install command.
When you add a driver that shells out to a tool, add the same preflight —
detect, point at the install, never auto-install (users are developers).

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
core/manifest/         dabs.json load + defaults.
core/actions/          ALL policy: manifest loading, instance-name
                       resolution across the fleet, --force/--dry, routing
                       by target, user-facing messages.
core/sandbox/          mechanical driver contract — exact names in, state
                       out. Zero vendor imports, zero logic.
core/sandbox/<kind>/   one driver per kind (apple, bwrap, server). Drivers
                       do no resolution, no policy, no messaging.
core/mcpserve/         the dabash MCP server, pure over an injected exec.
```

**Rules that keep it clean**

- Zero third-party dependencies. `go.mod` has no require block; keep it so.
- `cli` and `core/actions` never import each other — they meet only in main.
  Drivers import only `core/sandbox`; nothing imports a driver except the
  build-tagged selection files.
- Drivers stay mechanical. New policy (resolution, force/dry, aggregation)
  goes in `core/actions`; a driver only ever takes exact names.
- New verb checklist: params struct + Actions method → action file →
  pure parser → command-table entry + runX → fake method in cli_test.go.
- The MCP server must never write non-protocol bytes to stdout — stdout is
  the protocol channel.
- Self-contained: no references to private projects, machines, usernames, or
  home paths anywhere (code, comments, tests, commit messages). Example
  names are neutral (`demo-0`, `myproj`).
- Commit messages say WHY, and for driver changes include what was run
  against the real system and what it printed.

**Git**

- Never commit or push unless explicitly told to. Make and verify the
  changes; leave committing and pushing to the human.
