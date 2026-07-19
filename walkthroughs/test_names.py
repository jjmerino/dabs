"""docs/guides/names.mdx — name a box or worktree, then reach it by that name.

`--name` gives the node a boot creates a handle of your choosing, which then
works everywhere an id does — `exec`, `ls`, `cd`, `rm`, `--worktree` — and shows
in the NODE column. `dabs cd <node>` prints that node's own directory as a bare
path, so a shell can step into it.
"""

from conftest import check, run


def test_name_your_work_and_reach_it(tut, dabs_home, repo):
    run(tut, "cd ~/repo")

    # Name a worktree: the name is the handle every message shows, and the
    # branch is cut after it (`dabs/<name>`), not a random id.
    check(tut, "dabs recipe wt --name login-fix", "names/cut")

    # Name a box over the repo: the boot line carries `id: dev`.
    check(tut, "dabs recipe sh --name dev --detach", "names/boot")

    # The name works everywhere an id does — reach into the box by name.
    check(tut, "dabs exec dev -- echo 'reached dev by name'", "names/exec")

    # Both named nodes read as the session they are: names in the NODE column.
    check(tut, "dabs ls", "names/ls")

    # `dabs cd` prints a node's own directory as a bare path — a prefix resolves
    # the same way every handle does.
    check(tut, "dabs cd dev", "names/cd")
    check(tut, "dabs cd de", "names/cd-prefix")

    # Wind the session down BY NAME: the box, then the worktree.
    check(tut, "dabs rm dev -y", "names/rm-box")
    check(tut, "dabs rm login-fix -y", "names/rm-worktree")


def test_a_name_held_by_active_work_is_refused(tut, repo):
    run(tut, "cd ~/repo")
    run(tut, "dabs recipe wt --name login-fix")

    # A name held by ACTIVE work refuses a new claim — never silently reused.
    check(tut, "dabs recipe wt --name login-fix; echo exit=$?", "names/name-taken")
