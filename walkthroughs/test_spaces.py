"""docs/guides/spaces.mdx — volume, held, tmp, and what rm does to each."""

import tuti

from conftest import check, grab, run, scrubbed

RECIPE = """\
recipes:
  spacey:
    description: demo box writing into all three spaces
    image: shell
    command: [sh]
    env: { GREETING: hello-spaces }
    sources:
      - mount: .
        path: /work
      - mkmount: $NODE_VOLUME/cache
        path: /cache
      - mkmount: $NODE_HELD/results
        path: /results
      - mkmount: $NODE_TMP/scratch
        path: /scratch
      - mkmount: $PARENT_VOLUME/keepers
        path: /keepers
"""


def test_three_spaces_three_fates(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(RECIPE)

    run(tut, "dabs recipe spacey --detach")
    box = grab(tut, r"\bspacey-[0-9a-f]{8}\b")

    check(
        tut,
        f"dabs exec {box} 'echo c > /cache/f; echo r > /results/f;"
        " echo s > /scratch/f; echo k > /keepers/f'; dabs ls",
        "spaces/ls-dots",
    )
    # rm without flags shows the per-space preview inside an interactive
    # Yes/No dialog; decline it to keep the box.
    tut.send_keys(f"clear; dabs rm {box}; echo :done-$((5*5))")
    tut.wait_until(tuti.contains("Yes"), 3000)
    tut.wait_until_settled(3000)
    scrubbed(tut.clip()).compare("spaces/rm-preview")
    tut.press("n")
    tut.wait_until(tuti.contains(":done-25"), 3000)
    check(tut, f"dabs rm {box} -y", "spaces/rm-keeps-volume")
    check(tut, f"dabs rm {box} -y --volume; dabs ls", "spaces/rm-volume")


def test_parent_volume_reloads_on_the_next_box(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(RECIPE)

    run(tut, "dabs recipe spacey --detach")
    box = grab(tut, r"\bspacey-[0-9a-f]{8}\b")
    run(tut, f"dabs exec {box} 'echo keep-me > /keepers/f; echo box-only > /cache/f'")
    run(tut, f"dabs rm {box} -y --volume")

    run(tut, "dabs recipe spacey --detach")
    box2 = grab(tut, r"\bspacey-[0-9a-f]{8}\b")
    check(tut, f"dabs exec {box2} 'cat /keepers/f; cat /cache/f'", "spaces/parent-volume-persists")

    run(tut, f"dabs rm {box2} -y --volume")
