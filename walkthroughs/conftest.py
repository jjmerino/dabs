"""tuti walkthrough suite for the dabs user manual.

Every screen shown in docs/ is asserted here against an approved golden, so
the manual is verifiable by a test run. The suite is written to run INSIDE a
dabseption-style box (dabs + tuti + tmux, HOME on a non-overlay filesystem) —
never on a host whose dabs state matters.

Each test gets its own throwaway HOME so its dabs node tree starts empty, with
the box's staged `shell` image copied in so recipes boot without a builder.
Node/instance ids and git hashes are random per run, so clips scrub them
before comparing; everything else on screen must reproduce exactly.
"""

from __future__ import annotations

import hashlib
import os
import re
import shutil
import subprocess
import zlib
from pathlib import Path

import pytest
import tuti

GOLDENS = Path(__file__).parent / "goldens"

# The home this test process runs under — it holds the staged `shell` image
# every per-test HOME is seeded from. Per-test HOMEs nest under a SHORT dir
# (`h/<6hex>`): the absolute path leaks into a few messages, and in the
# `worktrees ls` table dabs sizes the WORKTREE column to the RAW path before
# `scrubbed` folds it to `~`, so a longer home widens the residual header gap.
# Short keeps that gap small (it cannot erase it without a dabs-side ~ fold).
BOX_HOME = Path(os.environ.get("HOME", "/tmp/h"))
HOMES = BOX_HOME / "h"


def scrubbed(clip):
    """Blank out what changes on every run: instance ids (12 hex), node ids
    and branch suffixes (8 hex), and short git hashes (7 hex).

    Also fold this run's throwaway HOME to `~`. dabs abbreviates HOME to `~` in
    most output, but a few messages print a bare absolute path (`dabs cd`, a
    worktree's checkout dir, a missing-mount error). Left raw, that path is both
    non-reproducible AND long enough to wrap `dabs ls`/`worktrees` rows — so the
    fold keeps goldens stable and quotable verbatim in the docs."""
    return (
        clip.scrub(re.escape(str(HOMES)) + r"/[A-Za-z0-9_-]+", "~")
        .scrub(r"[0-9a-f]{12}", "____________")
        .scrub(r"[0-9a-f]{8}", "________")
        .scrub(r"\b[0-9a-f]{7}\b", "_______")
        .scrub(r":done-\d+", ":done")
    )


def run(tut, cmd, timeout=3000):
    """Clear the pane, run ``cmd``, and wait until it has finished.

    A settle-wait alone fires during a pause in the output (a box boot prints
    two lines, thinks, prints more), so the command is followed by an end
    marker the shell COMPUTES — the typed line never contains the token the
    wait looks for, only the finished screen does.
    """
    n = zlib.crc32(cmd.encode()) % 100000
    tut.send_keys(f"clear; {cmd}; echo :done-$(({n}+0))")
    tut.wait_until(tuti.contains(f":done-{n}"), timeout)
    tut.wait_until_settled(timeout)


def check(tut, cmd, key, timeout=3000):
    """Run ``cmd`` and assert the finished screen against the golden ``key``."""
    run(tut, cmd, timeout)
    return scrubbed(tut.clip()).compare(key)


def grab(tut, pattern):
    """Read a generated name (node id, instance id) off the live screen."""
    match = re.search(pattern, tut.clip().plain)
    assert match, f"nothing on screen matches {pattern!r}"
    return match.group(0)


def sh(args, cwd):
    subprocess.run(args, cwd=cwd, check=True, capture_output=True)


@pytest.fixture
def dabs_home(request):
    """A fresh HOME with an empty dabs state and the staged shell image."""
    # A short 6-hex slug of the test name: unique per test, but far shorter than
    # the test name itself (which would widen the leaked home path — see HOMES).
    slug = hashlib.sha1(request.node.name.encode()).hexdigest()[:6]
    home = HOMES / slug
    if home.exists():
        shutil.rmtree(home)
    (home / ".dabs").mkdir(parents=True)
    # Hardlink the staged images (cp -al): copying the 21MB shell rootfs per
    # test makes the first boot cold and slow; hardlinks keep the page cache hot.
    staged = BOX_HOME / ".dabs" / "images"
    if staged.exists():
        sh(["cp", "-al", str(staged), str(home / ".dabs" / "images")], home)
    (home / "myproj").mkdir()
    yield home
    # A failing test aborts before its own `dabs rm`, so reap whatever this
    # HOME still owns — a live box left behind is a process that outlives the
    # test and destabilizes every later run.
    env = dict(os.environ, HOME=str(home))
    ls = subprocess.run(["dabs", "ls"], env=env, capture_output=True, text=True).stdout
    # Reap every node by its NODE-column handle — the token before its KIND,
    # after any tree glyphs. This catches NAMED nodes (dev, login-fix) too, not
    # just generated ids; a hex-only match would leave a named live box behind.
    handles = re.findall(r"^[\s│├└─]*([A-Za-z0-9][\w-]*)\s+(?:project|workdir|worktree|box)\b", ls, re.M)
    for node in dict.fromkeys(handles):
        subprocess.run(
            ["dabs", "rm", node, "-y", "--force", "--volume", "--multiple"],
            env=env,
            capture_output=True,
        )
    shutil.rmtree(home, ignore_errors=True)


@pytest.fixture
def repo(dabs_home):
    """A tiny git repo at ~/repo for the worktree walkthroughs."""
    repo = dabs_home / "repo"
    repo.mkdir()
    sh(["git", "init", "-q", "-b", "main"], repo)
    sh(["git", "config", "user.email", "demo@example.com"], repo)
    sh(["git", "config", "user.name", "Demo"], repo)
    (repo / "app.txt").write_text("line one\n")
    sh(["git", "add", "."], repo)
    sh(["git", "commit", "-qm", "first"], repo)
    return repo


@pytest.fixture
def tut(dabs_home):
    """A terminal whose dabs sees only this test's HOME."""
    env = {
        "HOME": str(dabs_home),
        "PATH": os.environ["PATH"],
        "TERM": "xterm-256color",
    }
    # A wide pane so no row of `dabs ls`/`worktrees ls` ever wraps. Wrapping is
    # physical — tmux breaks the line on the RAW absolute path this test's long
    # temp HOME produces, before `scrubbed` folds it to `~` — so the pane must
    # fit that raw path even though the golden only ever shows the short `~` form
    # a real (short-HOME) user sees. A split row would escape the scrub and flap.
    with tuti.start(
        size=(240, 40), cwd=dabs_home / "myproj", env=env, color=True, goldens=GOLDENS
    ) as t:
        yield t
