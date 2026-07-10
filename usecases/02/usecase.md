# Recipe 02 — hand a feature to a work agent

**Use case:** I'm a Mac dev in my repo. I want to hand "add dark mode" to a Claude
agent that works **in isolation on its own branch** while I keep hacking on my
actual working tree. When it's done I want to review its branch like any other
PR — nothing it did should have leaked into my checkout, and nothing I do should
disturb it.

Why `-mw` (mounted worktree): I need **worktree** so the agent gets its own
branch and my working tree is untouched; I need **mount** (not copy) so the
agent's commits land on my host where I can `git log` and open a PR from them.
Copy would trap the work inside a box I have to exfiltrate; a bare `-m` on cwd
would let the agent scribble on the branch I'm using right now.

**Ideal flow:**

```bash
brew install dabs
dabs auth claude          # once per machine; reused by every agent forever

cd ~/code/myapp           # my real repo, on my real branch, mid-edit — untouched below

# Start a NAMED, DETACHED work agent on a fresh worktree. dabs cuts a branch
# (dabs/dark-mode) off HEAD under ~/.dabs/worktrees, mounts it live into a box,
# and sets the agent going on the task. My shell returns immediately.
dabs claude -mw --name dark-mode --detach \
  "Add a dark mode toggle to Settings. Match the existing theming. Commit as you go."

# I keep working. Meanwhile I peek whenever I want:
dabs ls
#  NAME        STATUS   BRANCH            SOURCE            DRIVER
#  dark-mode   working  dabs/dark-mode    ~/.dabs/wt/…      apple

dabs logs dark-mode -f          # follow its narration
# …or jump in and pair with it:
dabs attach dark-mode           # live session; Ctrl-b d to detach again

# When it says it's done, I review its branch — it's a real branch on my host
# because the worktree was MOUNTED, not copied:
git -C ~/.dabs/worktrees/myapp-dark-mode log --oneline
git -C ~/.dabs/worktrees/myapp-dark-mode diff main

# Happy → merge it like any branch. Then reap the agent (worktree is kept until I say):
git worktree remove ~/.dabs/worktrees/myapp-dark-mode
dabs down dark-mode
```

**What this pins down about the CLI:**

- `--name` + `--detach` turn `dabs claude` from a foreground REPL into a
  *managed agent* I can leave, list, and return to.
- `-mw` is the canonical "work agent" strategy — isolation (worktree) plus
  reviewability (mount). This is exactly what today's `dabs claude` already does;
  the recipe just names it as one point in the matrix so `-m`, `-w`, and plain
  copy become the other three.
- The worktree is **kept**, never auto-destroyed — an agent's work is never
  silently thrown away (see [03] for resuming it).
- `attach` and `logs -f` are the two ways to rejoin: pair interactively, or just
  watch.
