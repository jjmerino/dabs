---
name: dabs
description: Run a command or a sub-agent inside a disposable dabs sandbox — a fresh machine reached through one shell tool (dabash), with no access to the host. Use when the user says "run it in a dabs box", "in a sandbox", "hand this to a sub-agent in a box", or wants a command executed off the host in a throwaway environment.
---

# dabs — a disposable sandbox reached through one tool

dabs (dumb agent boxes) builds a box from a Dockerfile and hands out access
to it through a single tool, `dabash(command, cwd?)`. The box has no view of
your host; each `up` is a fresh instance.

## Directly

```bash
dabs build <dir-with-dabs.json>       # once per Dockerfile change
dabs up <dir>                         # → <name>-<hex> up   (capture the FULL name)
dabs run <instance> -- <cmd…>         # execute inside; shell syntax needs sh -c '…'
dabs down <instance>                  # remove it (--force for all of a name)
```

## Hand it to a sub-agent

`dabs mcp <full-instance>` is an MCP stdio server exposing only
`dabash`, curried to that box — the tool has no sandbox parameter, so the
sub-agent can reach nothing else:

```bash
claude --setting-sources "" --tools "" --strict-mcp-config \
  --mcp-config '{"mcpServers":{"dabash":{"command":"dabs","args":["mcp","<full-instance>"]}}}' \
  --allowedTools "mcp__dabash__dabash" \
  -p "<task>"
```

## Notes

- Pass the FULL instance name to `dabs mcp` (not a prefix) — an exact name
  resolves locally with no ssh probe, so the server starts instantly.
- `--setting-sources ""` keeps credentials but drops host config; `--bare`
  breaks auth. `--allowedTools` is required in `-p` mode or it stalls.
- Boxes are copies, not mounts: rebuild after editing source. A box only
  contains what its Dockerfile installed.
