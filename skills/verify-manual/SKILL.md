---
name: verify-manual
description: Re-verify the dabs manual against a RELEASED binary by driving the tuti walkthrough suite in the scribe box, re-blessing changed goldens, updating the docs to match verbatim, and rendering the screendiffs for review. Use when cutting a release, or whenever a change alters what the CLI prints (help, errors, recipes, ls/worktrees layout, boot/rm messages).
---

# Verify the dabs manual against a release

Every terminal screen in `docs/` is asserted by a tuti walkthrough
(`walkthroughs/test_<page>.py`) against an approved golden, and the docs quote
those goldens **verbatim**. The scribe box installs `releases/latest`, so the
goldens document what a user who just ran the installer actually sees. When the
released binary changes what it prints, the goldens and the docs drift from it —
this skill re-syncs them.

These are **doc-producing tests, not CI gates**: they need the privileged
`scribe` box (tuti + a released dabs + bun) and each test uses a throwaway
`$HOME`, so never run them on a host whose dabs state matters. Nothing in
GitHub Actions runs them.

## When to run

- **Cutting a release** — after the release is published (so `releases/latest`
  is the version to document). The manual must describe the released binary.
- **Any change to CLI output** — help text, error wording, recipe listing,
  `ls`/`worktrees` columns, boot/rm/consent messages, a new flag.

## The loop

Run from the repo root on the host; the suite runs INSIDE the box.

1. **Build the box** (re-fetches `releases/latest` via the Dockerfile's
   `ADD .../releases/latest` cache-bust):

   ```bash
   dabs build dabseption && dabs build scribe
   ```

2. **Boot it and run the suite:**

   ```bash
   dabs recipe scribe --detach --name scribe
   dabs exec scribe 'cd /work/walkthroughs && python3 -m pytest -q'
   ```

3. **Re-bless what legitimately changed.** A failing screen is either a doc bug
   or a real behaviour change; when it is the latter, save the runs and approve.
   A test with several `check()`s aborts at the FIRST mismatch, so one approve
   only advances one screen — loop until green:

   ```bash
   dabs exec scribe 'cd /work/walkthroughs && for i in $(seq 1 12); do
     TUTI_SAVE_RUNS=1 python3 -m pytest -q >/dev/null 2>&1
     tuti -g goldens review --approve >/dev/null 2>&1
     python3 -m pytest -q >/dev/null 2>&1 && { echo "green $i"; break; }
   done'
   ```

   `/work` is the live repo, so the re-blessed goldens land back on the host.

4. **Update the docs to match, verbatim.** For each changed golden, find the
   matching `docs/` code block and make it quote the golden's ANSI-stripped text.
   Trim ONLY: the two box-staging lines (`image shell: no build record —
   rebuilding` / `no builder here to refresh it — serving it as-is`), the
   `:done`/`#` markers, and the interactive-prompt footer. Paths are already
   folded to `~` by `scrubbed()`; never hand-elide a path or paste rows from two
   goldens into one block — quote the real screen.

5. **Render the screendiffs for review** (goldens are ANSI, unreadable raw):

   ```bash
   python3 walkthroughs/render_goldens.py > screendiffs.html   # colour gallery
   ```

6. **Reap the box and open a PR:**

   ```bash
   dabs rm scribe -y
   ```

## Adding a NEW journey

One page = one journey = one test module. To document a new behaviour:

1. Write `walkthroughs/test_<page>.py` that drives the REAL flow and asserts the
   teaching screens (see the existing modules for the `check`/`grab`/`run`
   helpers and the recipe/repo fixtures). Assert screens that would FAIL if the
   feature were broken — a journey that passes with the feature off proves
   nothing.
2. Bless its goldens (the save+approve loop above).
3. Write `docs/guides/<page>.mdx` from those goldens and add it to
   `docs/docs.json` nav. Add an `<Info>Screens verified by
   walkthroughs/test_<page>.py.</Info>` line.

## Notes

- The scribe box is privileged and nested (dabseption base); the dabs under test
  is the released binary, sandboxed in the box while you drive from the host.
- If a release has not actually published (`releases/latest` still points at the
  old tag), fix and publish the release FIRST — the scribe documents whatever
  the installer serves.
