"""docs/quickstart.mdx — boot a box, reach in, see the live mount, reap it."""

import tuti

from conftest import check, grab, run, scrubbed


def test_boot_reach_in_and_reap(tut):
    run(tut, "echo 'hello from my project' > note.txt")

    check(tut, "dabs recipe sh --detach", "quickstart/boot")
    box = grab(tut, r"\bsh-[0-9a-f]{8}\b")

    check(
        tut,
        f"dabs exec {box} 'cat /work/note.txt && echo boxed >> /work/note.txt'",
        "quickstart/exec",
    )
    check(tut, "cat note.txt", "quickstart/mount-is-live")
    check(tut, "dabs ls", "quickstart/ls")
    # Without -y, rm previews what it would reap and asks — on a terminal this
    # is an interactive Yes/No dialog. Decline it, then consent with -y.
    tut.send_keys(f"clear; dabs rm {box}; echo :done-$((5*5))")
    tut.wait_until(tuti.contains("Yes"), 3000)
    tut.wait_until_settled(3000)
    scrubbed(tut.clip()).compare("quickstart/rm-dialog")

    tut.press("n")
    tut.wait_until(tuti.contains(":done-25"), 3000)
    tut.wait_until_settled(3000)
    scrubbed(tut.clip()).compare("quickstart/rm-declined")

    check(tut, f"dabs rm {box} -y", "quickstart/rm")
