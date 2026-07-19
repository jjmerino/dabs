"""docs/guides/exec.mdx — the two exec forms, DABS_NAME, and the error paths."""

from conftest import check, grab, run


def test_shell_form_vs_exact_argv(tut, dabs_home):
    # Two files at /work so the shell form's glob has something to expand —
    # that is what makes the two forms produce visibly different output.
    proj = dabs_home / "myproj"
    (proj / "app.py").write_text("")
    (proj / "notes.md").write_text("")

    run(tut, "dabs recipe sh --detach")
    box = grab(tut, r"\bsh-[0-9a-f]{8}\b")

    # `--` is an exact argv: no shell, so `*` reaches echo verbatim.
    check(tut, f"dabs exec {box} -- echo '*'", "exec/argv-no-shell")
    # No `--` is a `sh -c` line: the shell expands `*` to the files at /work.
    check(tut, f"dabs exec {box} 'cd /work && echo *'", "exec/shell-glob")
    check(tut, f"dabs exec {box} 'echo I am $DABS_NAME'", "exec/dabs-name")

    run(tut, f"dabs rm {box} -y")


def test_unknown_node_is_an_error(tut):
    check(tut, "dabs exec nothere echo hi; echo exit=$?", "exec/unknown-node")
