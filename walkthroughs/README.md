# tuti walkthrough suite for the dabs manual

Every terminal screen in `../docs` is asserted here against an approved
[tuti](https://github.com/jjmerino/tuti) golden (`goldens/<page>/<screen>.txt`,
ANSI colour included), so the manual is verifiable by a test run. One test
module per docs page.

These are **doc-producing tests, not CI gates.** They drive a released dabs
binary inside a privileged nested-sandbox box to photograph its TUI; they are
run by hand when the docs change, never wired into GitHub Actions.

## Running

The suite drives a real dabs, so it runs **inside the `scribe` box** — never on
a host whose dabs state matters. From this repo's root (see `../dabs.yaml`):

```bash
dabs build dabseption && dabs build scribe     # base image, then tuti + released dabs
dabs recipe scribe --detach                    # dabs + tuti + tmux, /work = this repo
dabs exec <box> 'cd /work/walkthroughs && python3 -m pytest -q'
```

The box installs dabs with the official installer (`https://dabs.dev/install.sh`,
always `releases/latest`) and tuti from its official git source, so the goldens
document what a user who just installed dabs actually sees. To document a
different release, publish it as `latest` and rebuild `scribe`.

Requirements inside the box (all provided by the `scribe` image): dabs on PATH,
tmux, python3 with `tuti` and `pytest`, and `HOME` on a non-overlay filesystem
holding a staged `~/.dabs/images/shell`.

## Seeing the screens

Goldens are ANSI captures — unreadable as raw `.txt`. Render them all to one
self-contained, colour HTML gallery to eyeball what the suite asserts:

```bash
python3 walkthroughs/render_goldens.py > screendiffs.html   # stdlib only
```

## Design notes

- Each test gets a throwaway `HOME` (fresh dabs node tree), seeded with the
  staged `shell` image by hardlink; teardown reaps whatever the test left
  running so a failure never leaks live boxes into later tests.
- Generated ids and git hashes are scrubbed before comparing; every command is
  followed by a computed `:done` marker so waits key on the finished screen,
  never on the typed line. Waits are capped at 3s.
- Goldens are never written by tests. To bless a legitimate change:
  `TUTI_SAVE_RUNS=1 python3 -m pytest -q`, then `tuti -g goldens review
  --approve`, and update the matching docs page.
