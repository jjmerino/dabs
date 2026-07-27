"""docs/reference/cli.mdx (## info) — one node's full model.

`dabs info <node>` is the per-node counterpart of the fleet-wide `dabs ls`: the
node's kind and id, the place it marks (per kind — a project's source repo, a
box's own node dir), its box instance, the argv appended to the recipe's command
at boot, the three spaces with what each holds, and the recipe that provisioned
it from the snapshot taken at creation.

The screens here fail if any of that regresses: an `appended` row that stopped
being persisted, a space that stopped reporting what it holds, or a recipe
snapshot replaced by the live registry all change the screen.
"""

import tuti

from conftest import check, grab, run

# A kept box whose command takes appended tokens, so the boot persists an argv
# on the node and `info` has an `appended` row to show. `keep: true` is what
# leaves the node behind after the command exits.
RECIPE = """\
recipes:
  keeper:
    description: a kept box for the info walkthrough
    image: shell
    command: [echo]
    keep: true
    env: { GREETING: hello-info }
    sources:
      - mount: .
        path: /work
      - mkmount: $NODE_VOLUME/cache
        path: /cache
"""


def test_info_shows_one_nodes_full_model(tut, dabs_home):
    run(tut, "dabs recipe sh --name dev --no-command")

    # A box: location is its own node dir (where the three spaces live), and the
    # instance is the running box's own identity.
    check(tut, "dabs info dev", "info/box")

    # A project: the same verb, resolved per KIND — location is the source repo
    # the command ran from, not a node dir.
    run(tut, "dabs ls")
    project = grab(tut, r"myproj-[0-9a-f]{8}")
    check(tut, f"dabs info {project}", "info/project")

    # An unknown handle is refused rather than guessed at.
    check(tut, "dabs info nothere; echo exit=$?", "info/unknown-node")

    run(tut, "dabs rm dev -y")


def test_info_shows_the_appended_boot_command(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(RECIPE)

    tut.send_keys("clear; dabs recipe keeper boxed hello; echo :done-$((7*3))")
    tut.wait_until(tuti.contains("Proceed?"), 3000)
    tut.send_keys("y")
    tut.wait_until(tuti.contains(":done-21"), 3000)
    tut.wait_until_settled(3000)

    keeper = grab(tut, r"keeper-[0-9a-f]{8}")
    run(tut, f"dabs exec {keeper} 'echo cached > /cache/f'")

    # The tokens appended at boot are persisted on the node and shown HERE, and
    # only here: `dabs ls` never carries them, because an appended command can
    # carry a prompt or a secret. The volume the box just wrote into reads as
    # holding files; the untouched held and tmp spaces read as empty.
    check(tut, f"dabs info {keeper}", "info/appended")

    # ...and the fleet-wide listing shows no trace of the appended argv.
    check(tut, "dabs ls", "info/ls-hides-appended")

    run(tut, f"dabs rm {keeper} -y --volume")
