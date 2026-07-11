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
dabs run <instance> <shell…>          # run a shell command line inside (sh -c)
dabs exec <instance> -- <cmd…>        # exec an EXACT argv inside (no shell)
dabs down <instance>                  # remove it (--force for all of a name)
```

## Run an agent inside the box — with a recipe

Recipes do the plumbing: a recipe is a declarative box (image, mounts, env,
command). dabs ships ONE recipe, `sh` (a generic clean-box shell); `dabs recipes`
lists what's available. Tool-specific recipes aren't bundled — they live in your
`~/.dabs/recipes.yaml` or a project's `./dabs.yaml`. A Claude Code box mounts
YOUR auth vault, so it's yours to define; copy this into `~/.dabs/recipes.yaml`:

```yaml
recipes:
  claude:                            # dabs recipe claude
    image: claude
    command: [claude]
    env: { CLAUDE_CONFIG_DIR: /root/.claude }
    sources:
      - mount: ~/.dabs/auth/claude   # your shared vault — dabs mounts it, never copies
        path: /root/.claude
      - worktree: .                  # a fresh git branch of the cwd
        path: /work
```

**One-off command:** `dabs do <cmd…>` runs a command in a throwaway box via the
project `default:` recipe (or `sh` if there's no `dabs.yaml`), appending your
command to the recipe's; `dabs recipe <name> <cmd…>` does the same for a named
recipe. Since it runs an arbitrary command in a box, dabs shows the recipe and
the exact command and asks for a y/N confirmation first.

## Notes

- Boxes are copies, not mounts: rebuild after editing source. A box only
  contains what its Dockerfile installed.
- Keep the build context under your home directory. A context under
  `/private/tmp` (agent scratchpad) was empirically found to fail `dabs build`
  on macOS with `failed to compute cache key … not found` (2026-07-09); under
  home it worked.
