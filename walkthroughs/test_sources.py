"""docs/guides/sources.mdx — mount vs copy vs mkmount, and read-only mounts."""

from conftest import check, grab, run

RECIPE = """\
recipes:
  snapshot:
    description: a copied /work plus a read-only reference mount
    image: shell
    command: [sh]
    sources:
      - copy: .
        path: /work
      - mount: ./data
        path: /ref
        ro: true
"""


def test_copy_is_a_snapshot_and_ro_refuses_writes(tut, dabs_home):
    proj = dabs_home / "myproj"
    (proj / "dabs.yaml").write_text(RECIPE)
    (proj / "data").mkdir()
    (proj / "data" / "ref.txt").write_text("reference data\n")

    check(tut, "dabs recipe snapshot --detach", "sources/boot-copy")
    box = grab(tut, r"\bsnapshot-[0-9a-f]{8}\b")

    check(tut, f"dabs exec {box} 'echo boxed > /work/new.txt; ls /work'", "sources/copy-in-box")
    check(tut, "ls", "sources/host-untouched")
    check(tut, f"dabs exec {box} 'cat /ref/ref.txt; echo no > /ref/nope'", "sources/ro-refused")

    run(tut, f"dabs rm {box} -y")


BROKEN = """\
recipes:
  broken:
    description: a plain mount whose origin does not exist
    image: shell
    command: [sh]
    sources:
      - mount: ./does-not-exist
        path: /work
"""


def test_a_missing_mount_is_refused(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(BROKEN)

    # A plain `mount:` whose origin is missing is a typo, not a request to
    # create it — dabs refuses and points at `mkmount:`.
    check(tut, "dabs recipe broken --detach; echo exit=$?", "sources/missing-mount")
