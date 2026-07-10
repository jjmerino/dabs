# Recipe 03 — reconnect to an agent from yesterday

**Use case:** I started a work agent yesterday, closed my laptop, went home. Today
I want to find it, see what it did overnight, and drop back into its session —
without remembering any IDs. If the box got stopped (reboot, `dabs down`), I want
to bring the *same* agent back on the *same* worktree, with its conversation
history intact, not start from zero.

This works because an agent is **durable state, not a process**: its name, its
worktree branch on my host, and its Claude session history (in the shared vault)
all outlive any single box. Reconnecting is just re-pointing a box at that state.

**Ideal flow:**

```bash
brew install dabs      # (already installed; agents survive upgrades)

# What have I got? ls shows agents whether their box is running or stopped.
dabs ls
#  NAME        STATUS    BRANCH           LAST ACTIVE   DRIVER
#  dark-mode   detached  dabs/dark-mode   14h ago       apple
#  invoices    stopped   dabs/invoices    2d ago        apple

# Case A — still running, just detached. Read what it did, then rejoin:
dabs logs dark-mode | tail -50
dabs attach dark-mode

# Case B — its box is stopped. Resume it: same name, same worktree, same
# Claude session history (all persisted in the vault) — the agent picks up
# where it left off instead of onboarding fresh.
dabs up --resume invoices
dabs attach invoices

# If I only need an answer, not a seat, I can poke it without attaching:
dabs send invoices "status? what's blocking the PDF export?"
dabs logs invoices -f
```

**What this pins down about the CLI:**

- `dabs ls` must show **stopped** agents too, with enough context (branch, last
  active) to recognize one — an agent I can't find is an agent I've lost.
- `dabs up --resume <name>` is the inverse of teardown: rebind a fresh box to an
  existing agent's worktree + session vault. Because [02]'s worktree is *kept*
  and the session history lives in the mounted `~/.claude` vault, "resume" is
  well-defined, not best-effort.
- `attach` (take a seat), `logs` (read), and `send` (one-shot poke) are three
  distinct verbs for three distinct intents — none of them require an ID, only
  the human-chosen `--name`.
