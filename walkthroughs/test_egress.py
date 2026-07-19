"""docs/guides/egress.mdx — confine a box's outbound network: cut it, gate it
by host, or route it through a hook you write.

`egress` on a recipe controls what a box can reach. As a scalar it is `open`
(the default) or `none` (no network). As a map it routes ALL egress through the
dabs proxy engine, which enforces a host `allow`/`deny` gate and an optional
`http_proxy` chain of hooks that see — and can rewrite — every byte.
"""

from conftest import check, run

NONE = """\
recipes:
  offline:
    image: shell
    command: [sh]
    egress: none
    sources:
      - mount: .
        path: /work
"""

GATED = """\
recipes:
  gated:
    image: shell
    command: [sh]
    egress:
      allow: "example.com"
    sources:
      - mount: .
        path: /work
"""

# A hook sees every response chunk; here it discards the upstream body and
# emits one fixed line, so the box's fetch visibly passed through your code.
BROKER_TS = """\
export default () => ({
  onResponseChunk(chunk, ctx) {
    if (chunk === null) return;             // end of stream
    if (ctx.done) return Buffer.alloc(0);   // swallow the rest
    ctx.done = true;
    return Buffer.from("the broker rewrote this response\\n");
  },
});
"""

HOOKED = """\
recipes:
  hooked:
    image: shell
    command: [sh]
    egress:
      http_proxy:
        - tls: terminate
        - module: broker.ts
        - tls: originate
    sources:
      - mount: .
        path: /work
"""


def test_none_cuts_the_network(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(NONE)

    run(tut, "dabs recipe offline --detach --name offline", timeout=20000)
    # A box with no egress cannot even resolve a name.
    check(tut, "dabs exec offline 'curl -sS -m 8 https://example.com 2>&1 | head -1'", "egress/none", timeout=20000)
    run(tut, "dabs rm offline -y", timeout=15000)


def test_allow_gates_by_host(tut, dabs_home):
    (dabs_home / "myproj" / "dabs.yaml").write_text(GATED)

    run(tut, "dabs recipe gated --detach --name gated", timeout=20000)
    # The allowlist is a CONNECT gate: an allowed host reaches, any other is
    # refused before a byte leaves.
    check(
        tut,
        "dabs exec gated '"
        "curl -sf -m 15 -o /dev/null https://example.com && echo \"example.com  reached\" || echo \"example.com  blocked\";"
        "curl -sf -m 15 -o /dev/null https://github.com && echo \"github.com   reached\" || echo \"github.com   blocked\"'",
        "egress/allow", timeout=25000,
    )
    run(tut, "dabs rm gated -y", timeout=15000)


def test_a_hook_rewrites_egress(tut, dabs_home):
    proj = dabs_home / "myproj"
    (proj / "dabs.yaml").write_text(HOOKED)
    (proj / "broker.ts").write_text(BROKER_TS)

    run(tut, "dabs recipe hooked --detach --name hooked", timeout=25000)
    # The box's request is terminated, handed to the hook, then re-originated —
    # what returns is whatever the hook chose to emit.
    check(tut, "dabs exec hooked 'curl -s -m 20 http://example.com/'", "egress/broker", timeout=25000)
    run(tut, "dabs rm hooked -y", timeout=15000)
