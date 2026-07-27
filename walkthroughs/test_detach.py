"""docs/caveats.mdx (`--detach` does not mean "boot and run nothing").

`--detach` boots a NEW box, starts the recipe's OWN command inside it in the
background, and returns — the box is left up with the command alive and its
combined output going to `~/.dabs/nodes/<id>/tmp/detached.log`.

WHY THE SUCCESS PATH IS NOT ASSERTED HERE: detaching needs a driver whose box
carries a process of its own. This suite runs in a box whose only driver is
bwrap, and bwrap enters the box with a fresh bwrap per command, so a command
cannot outlive the call that started it (core/sandbox/bwrap: CheckDetach). A
detached run therefore cannot happen here at all, and faking one would document
a screen no user ever sees. What IS real here is every refusal, and each is a
different guard: the driver's own (asked BEFORE anything is provisioned), the
two-opposite-asks argv error, and the recipe-has-no-command error.
"""

from conftest import check, run

# A recipe with an image but no command: there is nothing for `--detach` to
# start, which is a different refusal from the driver's.
RECIPE = """\
recipes:
  quiet:
    description: a box with no command of its own
    image: shell
    sources:
      - mount: .
        path: /work
"""


def test_detach_is_refused_by_a_driver_that_cannot_hold_a_command(tut):
    # The driver is ASKED before the boot, so the refusal names the driver and
    # gives ITS reason — and points at the pair that does work here.
    check(tut, "dabs recipe sh --detach; echo exit=$?", "detach/driver-refuses")


def test_no_command_and_detach_are_opposite_asks(tut):
    # One boots a box and runs nothing; the other starts the recipe's command.
    # Passing both is an argv error, never a silent pick of one.
    check(tut, "dabs recipe sh --no-command --detach; echo exit=$?", "detach/both-flags")


def test_detach_appends_nothing(tut):
    # `--detach` starts the recipe's OWN command; a trailing token would be an
    # append, so it is refused rather than quietly dropped or run.
    check(tut, "dabs recipe sh --detach echo hi; echo exit=$?", "detach/no-append")


def test_a_recipe_with_no_command_has_nothing_to_detach(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(RECIPE)

    # Nothing to start — and the refusal names the flag that DOES boot a box
    # without a command.
    check(tut, "dabs recipe quiet --detach; echo exit=$?", "detach/no-command-to-run")

    # That flag then boots the same recipe fine, which is what makes the pointer
    # in the message above a real answer rather than a deflection.
    run(tut, "dabs recipe quiet --no-command")
    check(tut, "dabs ls", "detach/no-command-booted")

    run(tut, "dabs rm quiet -y --multiple")
