---
name: dabs
description: Run a command or an agent inside a disposable dabs sandbox — a fresh machine with no access to the host. Use when the user says "run it in a dabs box", "in a sandbox", or wants a command or agent executed off the host in a throwaway environment.
---

# dabs — a disposable sandbox

dabs (dumb agent boxes) builds a box from a Dockerfile and lets you run
commands — or a whole agent — inside it. The box has no view of your host;
each `up` is a fresh instance.

## Directly

```bash
dabs build <dir-with-dabs.json>       # once per Dockerfile change
dabs up <dir>                         # → <name>-<hex> up   (capture the FULL name)
dabs run <instance> -- <cmd…>         # execute inside; shell syntax needs sh -c '…'
dabs down <instance>                  # remove it (--force for all of a name)
```

## Run an agent inside the box — with a recipe

Recipes do the plumbing. `dabs recipe claude` runs Claude Code in a fresh box,
already authenticated. That recipe ships out of the box (`dabs recipes` lists
it); copy it into `~/.dabs/recipes.yaml` for your own:

```yaml
recipes:
  claude:                            # ships out of the box → dabs recipe claude
    image: claude
    command: [claude]
    env: { CLAUDE_CONFIG_DIR: /root/.claude }
    sources:
      - mount: ~/.dabs/auth/claude   # your shared vault — dabs mounts it, never copies
        path: /root/.claude
      - worktree: .                  # a fresh git branch of the cwd
        path: /work
```

## Notes

- Boxes are copies, not mounts: rebuild after editing source. A box only
  contains what its Dockerfile installed.
- Keep the build context under your home directory. A context under
  `/private/tmp` (agent scratchpad) was empirically found to fail `dabs build`
  on macOS with `failed to compute cache key … not found` (2026-07-09); under
  home it worked.
