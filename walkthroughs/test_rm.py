"""docs/guides/cleanup.mdx — prefixes, ambiguity, --multiple, --dry, --inactive."""

from conftest import check, run


def test_a_prefix_matching_many_reaps_nothing(tut):
    run(tut, "dabs recipe sh --detach")
    run(tut, "dabs recipe sh --detach")

    check(tut, "dabs rm sh -y; echo exit=$?", "rm/ambiguous-refused")
    check(tut, "dabs rm sh --dry --multiple", "rm/dry-multiple")
    check(tut, "dabs rm sh -y --multiple", "rm/multiple")


def test_inactive_markers_are_swept_separately(tut):
    run(tut, "dabs recipe sh --detach")
    check(tut, "dabs rm sh -y; dabs ls", "rm/empty-tree")
    check(tut, "dabs ls --inactive", "rm/ls-inactive")
    check(tut, "dabs rm --inactive; dabs ls --inactive", "rm/rm-inactive")
