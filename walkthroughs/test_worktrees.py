"""docs/guides/worktrees.mdx — cut, edit, judge, review, and reap worktrees."""

from conftest import check, grab, run


def test_worktree_lifecycle(tut, repo):
    run(tut, "cd ~/repo")

    check(tut, "dabs recipe wt", "worktrees/cut")

    check(tut, "dabs worktrees ls", "worktrees/no-diff")
    wt = grab(tut, r"\brepo-[0-9a-f]{8}\b")

    run(tut, f"dabs recipe sh --worktree {wt} --detach")
    box = grab(tut, r"\bsh-[0-9a-f]{8}\b")

    check(
        tut,
        f"dabs exec {box} 'cd /work && echo edit >> app.txt'; dabs worktrees ls",
        "worktrees/has-work",
    )
    check(tut, f"dabs worktrees diff {wt}", "worktrees/diff")
    check(
        tut,
        f"dabs exec {box} 'cd /work && git add -A && git commit -qm improve && git --no-pager log --oneline'",
        "worktrees/git-works-in-box",
    )
    check(tut, "dabs worktrees ls", "worktrees/unmerged")
    check(tut, f"dabs rm {wt} -y", "worktrees/rm-refuses-unreviewed")
    check(tut, f"dabs rm {wt} -y --force", "worktrees/rm-force")


def test_clean_sweep_reaps_only_reviewed(tut, repo):
    run(tut, "cd ~/repo")
    run(tut, "dabs recipe wt")
    run(tut, "dabs recipe wt")

    check(tut, "dabs rm --clean-worktrees; dabs worktrees ls", "worktrees/clean-sweep")
