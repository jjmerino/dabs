# AGENTS.md — delegating to dumb agents with dabs

You are (presumably) a capable agent with host access. dabs lets you spawn
DUMB agents: sub-agents locked inside a disposable box with exactly one
capability — a shell in that box. Use them to user-test a program the way a
naive human would: no host files, no config, no memory of how it was built.

Why bother: you know too much. You wrote the install script, so you can't
get honestly stuck. A dumb agent can. Its confusion is the signal.

## The loop

1. **Build the box image** (once per Dockerfile change):

   ```bash
   dabs build <dir-with-dabs.json>
   ```

2. **Boot a fresh instance per test run** — every `up` is a NEW pristine box;
   capture the instance name it prints:

   ```bash
   dabs up <dir>          # → myproj-a3f9c21d4e02 up
   ```

3. **Hand the box to a dumb agent.** `dabs mcp <instance>` is an MCP stdio
   server exposing ONE tool: `dabash(command, cwd?)`. The instance is bound
   at launch — the tool has no sandbox parameter, so the sub-agent cannot
   reach any other box or your host. Launch the sub-agent with no builtin
   tools and no user config:

   ```bash
   claude --setting-sources "" --tools "" --strict-mcp-config \
     --mcp-config '{"mcpServers":{"dabash":{"command":"dabs","args":["mcp","<instance>"]}}}' \
     --allowedTools "mcp__dabash__dabash" \
     -p "<task prompt — see below>"
   ```

   Flag notes, learned the hard way: `--setting-sources ""` drops user
   config but KEEPS credentials (`--bare` breaks auth). `--allowedTools` is
   required in `-p` mode or the run stalls on a permission prompt.

4. **Read the transcript, then reap:**

   ```bash
   dabs down <instance>            # or: dabs down <name> --force  (all instances)
   dabs down <name> --dry          # preview what a name matches
   ```

## Prompting the dumb agent

- Tell it the truth about its world: "You have exactly one tool, dabash,
  which runs shell commands inside your machine. There is no other
  filesystem or host."
- Give it a GOAL, not steps: "figure out how to <user journey>" — the point
  is watching it try, not having it execute your plan.
- Tell it to act like a first-time user: try the obvious command first,
  read error messages, use --help. Forbid reading the program's source to
  make progress — a real user wouldn't.
- Do NOT ask it to judge success. Actors drive and leave a trace; YOU (or a
  separate judge pass) score the trace afterward. Self-reported success
  inflates.
- One instance per sub-agent, one journey per instance. Instances are cheap
  (`dabs up` again) and isolated; sharing a box couples runs.

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
directory containing `dabs.json`.
