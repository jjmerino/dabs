# Recipe 05 — iterate on a local CLI with a live agent

**Use case:** I'm writing a small tool right now, in a scratch directory that
isn't even a git repo yet. I want a Claude agent editing the *actual files I have
open*, running the thing in a clean box (right toolchain, no "works on my
machine"), while I watch the diffs land in my editor in real time. This is the
opposite of [01]/[02]: I *want* the blast radius. I'm pairing, not delegating.

Why `-m` (mount cwd, no worktree): **mount** so edits flow both ways instantly —
the agent's changes hit my host, my changes hit the box. **cwd, not worktree**,
because there's no branch to isolate onto (it's not even a repo) and I explicitly
want the agent in my working directory, not a copy of it. This is the one case
where copy would be *wrong*: a snapshot means the agent's fixes are stranded in a
box while my editor shows the old code.

**Ideal flow:**

```bash
brew install dabs
dabs auth claude

cd ~/scratch/csv2json     # a few .py files, no .git, half-working

# Mount cwd live into a fresh Python box and start pairing. Edits are bidirectional.
dabs claude -m "This csv2json script chokes on quoted commas. Fix it and add tests."

#  … in the box, the agent edits ./csv2json.py and ./test_csv2json.py …
#  … my editor, open on the SAME files on the host, updates as it types …

# I can run the box's clean toolchain against the live files without leaving my seat:
#   (from another terminal)
dabs run <that-box> -- python -m pytest -q      # green, against the mounted files

# I tweak a line in my editor; the agent sees it next turn. When I'm done I just
# exit the agent. My files are already updated in place — nothing to extract,
# nothing to merge. The box vanishes; the code stays.
```

**Contrast — the same task the other three ways, and why they'd be wrong here:**

| Command | `/work` is | What happens to my fix |
|---|---|---|
| `dabs claude` (copy cwd) | a snapshot | stuck in the box; I'd have to copy it out |
| `dabs claude -m` (mount cwd) | **my live files** | **lands in my editor instantly ← this recipe** |
| `dabs claude -w` (copy worktree) | a branch snapshot | needs `git` + an extract step |
| `dabs claude -mw` (mount worktree) | a live branch | great for a *repo*, overkill for a scratch dir |

**What this pins down about the CLI:**

- `-m` is the "pairing" strategy: the box is just a clean, correct *runtime* wrapped
  around the files I already have. No copy, no branch, no ceremony.
- The four commands in the table are the whole design — one flag pair (`m`, `w`)
  over `dabs claude`, each cell a real, distinct use case. If a fifth behavior is
  wanted, it belongs in a `dabs.json` ([01]), not a fifth flag.
- Mounting a non-git directory is fine — the worktree machinery is only pulled in
  by `-w`/`-mw`, so `-m` has no repo requirement (unlike bare `dabs claude` on a
  repo, which [02]/[03] build on).
