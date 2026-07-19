"""docs/guides/recipes.mdx — listing, authoring, appended commands, confirmation."""

import tuti

from conftest import check, run, scrubbed

RECIPE = """\
default: hello
recipes:
  hello:
    description: demo box for the recipes walkthrough
    image: shell
    command: [sh]
    env: { GREETING: hello-recipes }
    sources:
      - mount: .
        path: /work
"""


def test_project_recipes_join_the_registry(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(RECIPE)
    check(tut, "dabs recipes", "recipes/list")


def test_appending_a_command_asks_first(tut):
    tut.send_keys("clear; dabs recipe sh -c 'echo appended to sh'; echo :done-$((7*3))")
    tut.wait_until(tuti.contains("Proceed?"), 3000)
    tut.wait_until_settled(3000)
    scrubbed(tut.clip()).compare("recipes/confirm-prompt")

    tut.send_keys("y")
    tut.wait_until(tuti.contains(":done-21"), 3000)
    tut.wait_until_settled(3000)
    scrubbed(tut.clip()).compare("recipes/appended-ran")


def test_declining_runs_nothing(tut):
    tut.send_keys("clear; dabs recipe sh -c 'echo should not run'; echo :done-$((7*3))")
    tut.wait_until(tuti.contains("Proceed?"), 3000)
    tut.send_keys("n")
    tut.wait_until(tuti.contains(":done-21"), 3000)
    tut.wait_until_settled(3000)
    scrubbed(tut.clip()).compare("recipes/declined")


def test_a_typo_never_becomes_a_command(tut):
    check(tut, "dabs recipe nosuch echo hi; echo exit=$?", "recipes/unknown-recipe")
