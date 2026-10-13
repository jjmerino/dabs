// The upstream proxy setting: an engine that reaches every destination through
// another HTTP proxy instead of the network, and refuses local targets while it
// does. Every destination here is a `.test` name, which never resolves, so a
// passing test cannot have dialed the network by accident: only the fake
// upstream knows where those names lead.

import { test, expect, afterEach } from "bun:test";
import { mkdtempSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import * as net from "node:net";
import * as http from "node:http";
import { start, parseUpstream, upstreamTarget, dialUpstream, ensureCA, leafFor, parsePrefix, type Prefix } from "./engine.ts";

const cleanups: (() => void)[] = [];
afterEach(() => { while (cleanups.length) cleanups.pop()!(); });

function listen(server: net.Server): Promise<number> {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve((server.address() as net.AddressInfo).port)));
}

// fakeUpstream is an HTTP proxy that only speaks CONNECT. It records every
// CONNECT line and sends each tunnel to the local port `routes` names for its
// authority; an authority it has no route for gets `refusal`.
async function fakeUpstream(routes: Record<string, number>, refusal = "HTTP/1.1 403 Forbidden\r\n\r\n") {
  const seen: string[] = [];
  const server = net.createServer((c) => {
    c.on("error", () => {});
    c.once("data", (d) => {
      const line = d.toString("latin1").split("\r\n")[0];
      seen.push(line);
      const m = line.match(/^CONNECT (\S+) HTTP\/1\.1$/);
      const port = m ? routes[m[1]] : undefined;
      if (port === undefined) { c.end(refusal); return; }
      const up = net.connect(port, "127.0.0.1", () => { c.write("HTTP/1.1 200 Connection Established\r\n\r\n"); c.pipe(up); up.pipe(c); });
      up.on("error", () => c.destroy());
    });
  });
  const port = await listen(server);
  cleanups.push(() => server.close());
  return { port, seen };
}

// echoTarget answers each chunk with "echo:" and the chunk, and records what it got.
async function echoTarget() {
  const got: string[] = [];
  const server = net.createServer((c) => { c.on("error", () => {}); c.on("data", (d) => { got.push(d.toString()); c.write("echo:" + d.toString()); }); });
  const port = await listen(server);
  cleanups.push(() => server.close());
  return { port, got };
}

// engineWith starts an engine in this process. An upstream left out of cfg is
// left out of the engine's config too, so the engine reads
// DABS_UPSTREAM_PROXY; pass "" for direct mode.
async function engineWith(cfg: { allow?: string[]; upstream?: string; chain?: object[]; allowLocal?: string[]; networks?: () => Prefix[] | null }) {
  const work = mkdtempSync(join(tmpdir(), "dabs-upstream-"));
  const socket = join(work, "engine.sock");
  const log = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = log;
  const engine = await start({ socket, caDir: join(work, "ca"), allow: cfg.allow ?? ["*"], chain: (cfg.chain ?? []) as never, ...("upstream" in cfg ? { upstream: cfg.upstream } : {}), ...(cfg.allowLocal ? { allowLocal: cfg.allowLocal } : {}), ...(cfg.networks ? { networks: cfg.networks } : {}) });
  delete process.env.DABS_CONNECT_LOG;
  cleanups.push(() => engine.stop());
  const ledger = () => (existsSync(log) ? readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : []);
  return { socket, ledger };
}

// connectThrough plays the box: CONNECT to the engine, and on a 200 send one
// chunk and wait for one answer. It resolves with the status line and the
// answer ("" when the tunnel closed without one).
function connectThrough(socket: string, authority: string, payload = "ping"): Promise<{ status: string; reply: string }> {
  return new Promise((resolve) => {
    const c = net.connect(socket);
    let buf = "";
    let status = "";
    c.on("error", () => {});
    c.on("connect", () => c.write(`CONNECT ${authority} HTTP/1.1\r\nHost: ${authority}\r\n\r\n`));
    c.on("data", (d) => {
      buf += d.toString("latin1");
      if (!status) {
        const end = buf.indexOf("\r\n\r\n");
        if (end < 0) return;
        status = buf.slice(0, buf.indexOf("\r\n"));
        buf = buf.slice(end + 4);
        if (!/ 200 /.test(status)) return;
        c.write(payload);
      }
      if (buf) { resolve({ status, reply: buf }); c.destroy(); }
    });
    c.on("close", () => resolve({ status: status || "(closed)", reply: buf }));
  });
}

function plainGet(socket: string, url: string): Promise<string> {
  return new Promise((resolve) => {
    const c = net.connect(socket);
    let buf = "";
    c.on("error", () => {});
    c.on("connect", () => c.write(`GET ${url} HTTP/1.1\r\nHost: ${new URL(url).host}\r\n\r\n`));
    c.on("data", (d) => { buf += d.toString("latin1"); });
    c.on("close", () => resolve(buf));
  });
}

test("parseUpstream: empty is direct mode, a plain http URL is an upstream", () => {
  expect(parseUpstream(undefined)).toBeNull();
  expect(parseUpstream("  ")).toBeNull();
  expect(parseUpstream("http://127.0.0.1:18080")).toEqual({ host: "127.0.0.1", port: 18080 });
  expect(parseUpstream("http://proxy.internal/")).toEqual({ host: "proxy.internal", port: 80 });
  expect(parseUpstream("http://[::1]:3128")).toEqual({ host: "::1", port: 3128 });
});

test("parseUpstream: anything else is refused by name, so the engine never falls back to dialing out", async () => {
  expect(() => parseUpstream("127.0.0.1:18080")).toThrow("is not a URL");
  expect(() => parseUpstream("socks5://127.0.0.1:1080")).toThrow("only http:// is supported");
  expect(() => parseUpstream("https://proxy:443")).toThrow("only http:// is supported");
  expect(() => parseUpstream("http://u:p@proxy:3128")).toThrow("credentials in the URL");
  expect(() => parseUpstream("http://proxy:3128/x")).toThrow("give only scheme, host and port");
  expect(() => parseUpstream("not a url")).toThrow("is not a URL");
  const work = mkdtempSync(join(tmpdir(), "dabs-upstream-"));
  await expect(start({ socket: join(work, "e.sock"), caDir: join(work, "ca"), chain: [], upstream: "https://proxy:443" })).rejects.toThrow("only http:// is supported");
  expect(existsSync(join(work, "e.sock"))).toBe(false);
});

// The engine does not resolve names in upstream mode, so upstreamTarget is the
// whole check: a literal against the direct mode's table, a localhost name by
// name, and anything that is neither a name nor an address.
test("upstreamTarget: every spelling of a refused address, a localhost name, and nothing else", () => {
  const refused = (h: string) => { try { upstreamTarget(h, [], []); return false; } catch { return true; } };
  for (const h of ["localhost", "LOCALHOST.", "app.localhost", "127.0.0.1", "127.9.9.9", "127.1", "2130706433", "0x7f.0.0.1", "0177.0.0.1", "0.0.0.0", "0",
    "169.254.169.254", "::1", "[::1]", "::", "[::]", "0:0:0:0:0:0:0:1", "::ffff:127.0.0.1", "fe80::1", "fe80::1%eth0", "febf::1", "FE80::1",
    // The hex forms the URL parser writes for an IPv4 inside IPv6, and the
    // dotted forms it reads them from.
    "[::ffff:7f00:1]", "::ffff:7f00:1", "::7f00:1", "::127.0.0.1", "::ffff:a9fe:a9fe", "::ffff:0.0.0.0", "0:0:0:0:0:ffff:127.0.0.1",
    "::ffff:0:7f00:1", "64:ff9b::7f00:1", "64:ff9b::127.0.0.1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::8.8.8.8", "2002:7f00:1::1", "2002:a9fe:a9fe::",
    // Private and other local ranges, refused as in direct mode.
    "10.0.0.1", "192.168.1.1", "172.17.0.1", "100.64.0.1", "fec0::1", "fec0::7f00:1", "fd00::1", "::808:808",
    // Not an IPv6 address, but has a colon.
    "::ffff:zz", "1:2:3", "[not-v6]"]) {
    expect([h, refused(h)]).toEqual([h, true]);
  }
  for (const h of ["example.com", "localhost.example.com", "mylocalhost", "128.0.0.1", "8.8.8.8",
    "2001:db8::1", "::ffff:8.8.8.8", "::ffff:808:808", "64:ff9b::808:808", "2002:808:808::1", "2001:db8::7f00:1",
    "fe7f::1", "169.255.0.1", "[2001:db8::1]"]) {
    expect([h, refused(h)]).toEqual([h, false]);
  }
  // A literal goes to the upstream in its standard form, a name as it was.
  expect(upstreamTarget("134744072", [], [])).toBe("8.8.8.8");
  expect(upstreamTarget("[2001:DB8::1]", [], [])).toBe("2001:db8::1");
  expect(upstreamTarget("Origin.Test.", [], [])).toBe("origin.test");
  // allowLocal opens what it names here too.
  expect(upstreamTarget("127.0.0.1", [{ ip: [127, 0, 0, 1], bits: 32 }], [])).toBe("127.0.0.1");
});

test("DABS_UPSTREAM_PROXY from the environment is the upstream when the config names none", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({ "origin.test:443": target.port });
  process.env.DABS_UPSTREAM_PROXY = `http://127.0.0.1:${up.port}`;
  let engine;
  try { engine = await engineWith({}); } finally { delete process.env.DABS_UPSTREAM_PROXY; }
  expect((await connectThrough(engine.socket, "origin.test:443")).reply).toBe("echo:ping");
  expect(up.seen).toEqual(["CONNECT origin.test:443 HTTP/1.1"]);
  // and the local refusal that comes with an upstream is on
  expect((await connectThrough(engine.socket, `127.0.0.1:${target.port}`)).status).toBe("HTTP/1.1 403 Forbidden");
  expect(target.got).toEqual(["ping"]);
});

test("an upstream of \"\" in the config is direct mode, even with DABS_UPSTREAM_PROXY set", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({});
  process.env.DABS_UPSTREAM_PROXY = `http://127.0.0.1:${up.port}`;
  let engine;
  // Direct mode refuses loopback too; the opt-in lets the engine dial the
  // test's own target, so the tunnel shows which way it went.
  try { engine = await engineWith({ upstream: "", allowLocal: ["127.0.0.1"] }); } finally { delete process.env.DABS_UPSTREAM_PROXY; }
  expect((await connectThrough(engine.socket, `127.0.0.1:${target.port}`)).reply).toBe("echo:ping");
  expect(up.seen).toEqual([]);
});

test("dialUpstream gives up on an upstream that never answers", async () => {
  const silent = net.createServer((c) => { c.on("error", () => {}); });
  const port = await listen(silent);
  cleanups.push(() => silent.close());
  // A liveness bound, not a benchmark: without the timer the dial never
  // settles, and the race ends on the guard instead.
  const outcome = await Promise.race([
    dialUpstream({ host: "127.0.0.1", port }, "slow.test", 443, 200).then(() => "connected", (e) => (e as Error).message),
    Bun.sleep(3000).then(() => "still waiting after 3 s"),
  ]);
  expect(outcome).toBe(`upstream proxy 127.0.0.1:${port} did not answer CONNECT slow.test:443 within 200 ms`);
});

test("IPv6 literals: one pair of brackets to the upstream, and a local one refused in every spelling", async () => {
  const target = await echoTarget();
  const got: string[] = [];
  const origin = http.createServer((q, s) => { got.push(`${q.method} ${q.url} host=${q.headers.host}`); s.end("from origin"); });
  const originPort = await listen(origin);
  cleanups.push(() => origin.close());
  const up = await fakeUpstream({ "[2001:db8::1]:443": target.port, "[2001:db8::2]:80": originPort });
  const { socket, ledger } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });

  const r = await connectThrough(socket, "[2001:db8::1]:443");
  expect(r.status).toBe("HTTP/1.1 200 Connection Established");
  expect(r.reply).toBe("echo:ping");
  const answer = await plainGet(socket, "http://[2001:db8::2]/v6");
  expect(answer).toContain("from origin");
  expect(got).toEqual(["GET /v6 host=[2001:db8::2]"]);

  for (const authority of ["[::1]:8080", "[::ffff:7f00:1]:8080", "[::ffff:127.0.0.1]:8080", "[::7f00:1]:8080", "[64:ff9b::7f00:1]:8080", "[::ffff:a9fe:a9fe]:80", "[fe80::1]:22"]) {
    const d = await connectThrough(socket, authority);
    expect([authority, d.status]).toEqual([authority, "HTTP/1.1 403 Forbidden"]);
  }
  expect(await plainGet(socket, "http://[::ffff:7f00:1]:8080/")).toStartWith("HTTP/1.1 403");
  expect(up.seen).toEqual(["CONNECT [2001:db8::1]:443 HTTP/1.1", "CONNECT [2001:db8::2]:80 HTTP/1.1"]);
  expect(ledger().filter((e) => e.verdict === "deny-address").length).toBe(8);
});

test("a CONNECT tunnel goes through the upstream and the bytes arrive", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({ "origin.test:443": target.port });
  const { socket, ledger } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });

  const r = await connectThrough(socket, "origin.test:443", "ping");
  expect(r.status).toBe("HTTP/1.1 200 Connection Established");
  expect(r.reply).toBe("echo:ping");
  expect(up.seen).toEqual(["CONNECT origin.test:443 HTTP/1.1"]);
  expect(target.got).toEqual(["ping"]);
  // Through an upstream the ledger names the upstream, not an address.
  expect(ledger().map((e) => [e.verdict, e.upstream, e.address])).toEqual([["tunnel", `127.0.0.1:${up.port}`, undefined]]);
});

test("with an upstream, a box cannot reach loopback: not the upstream's own port, not localhost, not by plain HTTP", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({ "origin.test:443": target.port, [`127.0.0.1:${target.port}`]: target.port, [`localhost:${target.port}`]: target.port });
  const { socket, ledger } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });

  // The anchor: the same engine does tunnel an allowed remote host, so the
  // refusals below are about the target and not a dead engine.
  expect((await connectThrough(socket, "origin.test:443")).reply).toBe("echo:ping");

  for (const authority of [`127.0.0.1:${up.port}`, `127.0.0.1:${target.port}`, `localhost:${target.port}`, `127.1:${target.port}`, `2130706433:${target.port}`]) {
    const r = await connectThrough(socket, authority);
    expect([authority, r.status]).toEqual([authority, "HTTP/1.1 403 Forbidden"]);
  }
  expect(await plainGet(socket, `http://127.0.0.1:${target.port}/`)).toStartWith("HTTP/1.1 403");
  // The upstream was asked once, for the anchor, and the target got only the anchor's bytes.
  expect(up.seen).toEqual(["CONNECT origin.test:443 HTTP/1.1"]);
  expect(target.got).toEqual(["ping"]);
  expect(ledger().filter((e) => e.verdict === "deny-address").length).toBe(6);
});

// 203.0.113.0/24 is a documentation range the address table does not refuse,
// standing in for a public network this host has an interface on.
test("with an upstream, a literal on a network of this host's interfaces is refused with the reason", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({ "198.51.100.7:443": target.port, "203.0.113.9:443": target.port });
  const { socket, ledger } = await engineWith({ upstream: `http://127.0.0.1:${up.port}`, networks: () => [parsePrefix("203.0.113.1/24")!] });
  // The anchor: an address off that network goes through.
  expect((await connectThrough(socket, "198.51.100.7:443")).reply).toBe("echo:ping");
  const r = await connectThrough(socket, "203.0.113.9:443");
  expect(r.status).toBe("HTTP/1.1 403 Forbidden");
  expect(up.seen).toEqual(["CONNECT 198.51.100.7:443 HTTP/1.1"]);
  expect(ledger().filter((e) => e.verdict === "deny-address").map((e) => e.reason)).toEqual(["203.0.113.9 is 203.0.113.9, on 203.0.113.1/24, a network of this host's interfaces; egress never reaches this host, its networks or a private range"]);
});

test("an IP literal in another spelling reaches the upstream in its standard form", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({ "8.8.8.8:443": target.port });
  const { socket } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });
  expect((await connectThrough(socket, "134744072:443")).reply).toBe("echo:ping");
  expect(up.seen).toEqual(["CONNECT 8.8.8.8:443 HTTP/1.1"]);
});

test("the static policy still runs first: a host outside allow never reaches the upstream", async () => {
  const target = await echoTarget();
  const up = await fakeUpstream({ "origin.test:443": target.port, "other.test:443": target.port });
  const { socket } = await engineWith({ allow: ["origin.test"], upstream: `http://127.0.0.1:${up.port}` });
  expect((await connectThrough(socket, "origin.test:443")).reply).toBe("echo:ping");
  expect((await connectThrough(socket, "other.test:443")).status).toBe("HTTP/1.1 403 Forbidden");
  expect(up.seen).toEqual(["CONNECT origin.test:443 HTTP/1.1"]);
});

// rawExchange sends one request to the engine and resolves with everything
// that comes back until the engine closes.
function rawExchange(socket: string, request: string): Promise<string> {
  return new Promise((resolve) => {
    const c = net.connect(socket, () => c.write(request));
    let buf = "";
    c.on("error", () => {});
    c.on("data", (d) => { buf += d.toString("latin1"); });
    c.on("close", () => resolve(buf));
  });
}
const connectRequest = (authority: string) => `CONNECT ${authority} HTTP/1.1\r\nHost: ${authority}\r\n\r\n`;
const getRequest = (url: string) => `GET ${url} HTTP/1.1\r\nHost: ${new URL(url).host}\r\n\r\n`;
const headOf = (response: string) => response.split("\r\n\r\n")[0];
const bodyOf = (response: string) => response.slice(response.indexOf("\r\n\r\n") + 4);
const header = (response: string, name: string) => headOf(response).split("\r\n").filter((l) => l.toLowerCase().startsWith(name.toLowerCase() + ": ")).map((l) => l.slice(name.length + 2));

test("an upstream's error answer reaches the box with its status and body, on both proxy paths", async () => {
  const body = "no tunnel for you\n";
  const up = await fakeUpstream({}, `HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nX-Other: dropped\r\nContent-Length: ${body.length}\r\n\r\n${body}`);
  const { socket, ledger } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });

  const tunnel = await rawExchange(socket, connectRequest("refused.test:443"));
  const plain = await rawExchange(socket, getRequest("http://refused.test/x"));
  for (const [what, r, authority] of [["CONNECT", tunnel, "refused.test:443"], ["GET", plain, "refused.test:80"]]) {
    expect([what, r.split("\r\n")[0]]).toEqual([what, "HTTP/1.1 403 Forbidden"]);
    expect([what, bodyOf(r)]).toEqual([what, body]);
    expect([what, header(r, "Content-Type")]).toEqual([what, ["text/plain"]]);
    expect([what, header(r, "X-Other")]).toEqual([what, []]);
    // The upstream gave no reason header of its own, so the engine adds one,
    // and it is a failure, not this engine's refusal.
    expect([what, header(r, "X-Dabs-Egress-Refused")]).toEqual([what, []]);
    expect([what, header(r, "X-Dabs-Egress-Failed")]).toEqual([what, [`upstream proxy refused CONNECT ${authority}: HTTP/1.1 403 Forbidden`]]);
  }
  expect(up.seen).toEqual(["CONNECT refused.test:443 HTTP/1.1", "CONNECT refused.test:80 HTTP/1.1"]);
  expect(ledger().filter((e) => e.t === "connect").map((e) => [e.verdict, e.proto, e.reason])).toEqual([
    ["upstream-refused", "connect", "upstream proxy refused CONNECT refused.test:443: HTTP/1.1 403 Forbidden"],
    ["upstream-refused", "http", "upstream proxy refused CONNECT refused.test:80: HTTP/1.1 403 Forbidden"],
  ]);
});

// The parent here is a real engine in direct mode, reached over TCP: the
// child's upstream setting takes a host and port, and an engine listens on a
// unix socket, so a small bridge joins the two.
test("a parent engine's refusal and failure reach the box with the parent's reason", async () => {
  const target = await echoTarget();
  const work = mkdtempSync(join(tmpdir(), "dabs-upstream-parent-"));
  const parentNames: Record<string, string[]> = { "ok.test": ["127.0.0.1"], "inner.test": ["10.0.0.5"] };
  const parent = await start({
    socket: join(work, "parent.sock"), caDir: join(work, "parent-ca"), allow: ["*"], chain: [], upstream: "", allowLocal: ["127.0.0.1"],
    resolve: async (h) => { if (!parentNames[h]) throw new Error(`no test name ${h}`); return parentNames[h]; },
    connect: (address, port) => net.connect({ host: address, port: address === "127.0.0.1" ? target.port : port }),
  });
  cleanups.push(() => parent.stop());
  const bridge = net.createServer((c) => { c.on("error", () => {}); const p = net.connect(join(work, "parent.sock")); p.on("error", () => c.destroy()); c.pipe(p); p.pipe(c); });
  const bridgePort = await listen(bridge);
  cleanups.push(() => bridge.close());
  const { socket } = await engineWith({ upstream: `http://127.0.0.1:${bridgePort}` });

  // The anchor: a destination the parent allows goes through both engines.
  expect((await connectThrough(socket, "ok.test:443")).reply).toBe("echo:ping");

  const refusal = "inner.test is 10.0.0.5, a private address (10.0.0.0/8); egress never reaches this host, its networks or a private range";
  for (const [what, r] of [["CONNECT", await rawExchange(socket, connectRequest("inner.test:443"))], ["GET", await rawExchange(socket, getRequest("http://inner.test/"))]]) {
    expect([what, r.split("\r\n")[0]]).toEqual([what, "HTTP/1.1 403 Forbidden"]);
    expect([what, header(r, "X-Dabs-Egress-Refused")]).toEqual([what, [refusal]]);
    expect([what, header(r, "X-Dabs-Egress-Failed")]).toEqual([what, []]);
    expect([what, bodyOf(r)]).toEqual([what, refusal + "\n"]);
  }
  const failure = "looking up nowhere.test: no test name nowhere.test";
  for (const [what, r] of [["CONNECT", await rawExchange(socket, connectRequest("nowhere.test:443"))], ["GET", await rawExchange(socket, getRequest("http://nowhere.test/"))]]) {
    expect([what, r.split("\r\n")[0]]).toEqual([what, "HTTP/1.1 502 Bad Gateway"]);
    expect([what, header(r, "X-Dabs-Egress-Failed")]).toEqual([what, [failure]]);
    expect([what, header(r, "X-Dabs-Egress-Refused")]).toEqual([what, []]);
    expect([what, bodyOf(r)]).toEqual([what, failure + "\n"]);
  }
  expect(target.got).toEqual(["ping"]);
});

test("an upstream's 407 reaches the box as a 502", async () => {
  const up = await fakeUpstream({}, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\nContent-Length: 0\r\n\r\n");
  const { socket } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });
  const r = await rawExchange(socket, connectRequest("auth.test:443"));
  expect(r.split("\r\n")[0]).toBe("HTTP/1.1 502 Bad Gateway");
  expect(header(r, "Proxy-Authenticate")).toEqual([]);
  expect(header(r, "X-Dabs-Egress-Failed")).toEqual(["upstream proxy refused CONNECT auth.test:443: HTTP/1.1 407 Proxy Authentication Required"]);
});

test("an upstream that answers an error and keeps the connection open does not hold the box", async () => {
  const body = "refused\n";
  const server = net.createServer((c) => { c.on("error", () => {}); c.once("data", () => c.write(`HTTP/1.1 403 Forbidden\r\nContent-Length: ${body.length}\r\n\r\n${body}`)); });
  const port = await listen(server);
  cleanups.push(() => server.close());
  const { socket } = await engineWith({ upstream: `http://127.0.0.1:${port}` });
  // A liveness bound, not a benchmark: an engine that waited for the close
  // would answer after UPSTREAM_ANSWER_MS (30 s), and the race ends on the
  // guard instead.
  const r = await Promise.race([rawExchange(socket, connectRequest("open.test:443")), Bun.sleep(3000).then(() => "still waiting after 3 s")]);
  expect(r.split("\r\n")[0]).toBe("HTTP/1.1 403 Forbidden");
  expect(bodyOf(r)).toBe(body);
});

test("an upstream that cannot be reached is a 502 that says so and is not a refusal", async () => {
  const gone = net.createServer();
  const port = await listen(gone);
  await new Promise((r) => gone.close(r));
  const { socket } = await engineWith({ upstream: `http://127.0.0.1:${port}` });
  const r = await plainGet(socket, "http://plain.test/");
  expect(r).toStartWith("HTTP/1.1 502 Bad Gateway");
  const head = r.split("\r\n\r\n")[0];
  expect(head).not.toContain("X-Dabs-Egress-Refused");
  expect(head).toMatch(new RegExp(`\r\nX-Dabs-Egress-Failed: upstream proxy 127\\.0\\.0\\.1:${port}: .+`));
});

test("a plain forward-proxy request rides a CONNECT tunnel to port 80", async () => {
  const got: string[] = [];
  const origin = http.createServer((q, s) => { got.push(`${q.method} ${q.url} host=${q.headers.host}`); s.end("from origin"); });
  const originPort = await listen(origin);
  cleanups.push(() => origin.close());
  const up = await fakeUpstream({ "plain.test:80": originPort });
  const { socket, ledger } = await engineWith({ upstream: `http://127.0.0.1:${up.port}` });

  const answer = await plainGet(socket, "http://plain.test/x?y=1");
  expect(answer).toStartWith("HTTP/1.1 200");
  expect(answer).toContain("from origin");
  expect(up.seen).toEqual(["CONNECT plain.test:80 HTTP/1.1"]);
  expect(ledger().filter((e) => e.t === "connect").map((e) => [e.verdict, e.proto, e.upstream])).toEqual([["forward", "http", `127.0.0.1:${up.port}`]]);
  expect(got).toEqual(["GET /x?y=1 host=plain.test"]);
});

// The terminate window: the engine decrypts the box's TLS, and then has to
// originate TLS to the real host through the tunnel and check its
// certificate. That needs a CA the engine process trusts at launch, so the
// engine runs in a child bun with NODE_EXTRA_CA_CERTS. The child also carries
// HTTP_PROXY/HTTPS_PROXY pointing at a trap: bun's http client honours those,
// and a request that went there instead of through the tunnel would be a
// second, unchecked route out.
// A non-HTTP/1 stream inside the window (h2, a raw protocol) is re-originated
// untouched; raw.test checks that this path rides the upstream too, and a raw
// stream to forged.test that the origin's certificate is checked on it.
// The engine also removes the proxy variables from its own process.env, for
// module code that reads them. That does not change bun's own fetch and
// node:http, which read them once at start (measured on bun 1.4.2), so
// engine.go starts the engine without them; engine_env_test.go covers that.
test("a terminated HTTPS request is re-originated through the upstream, with the origin's certificate checked", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-upstream-tls-"));
  const tlsMod = await import("node:tls");
  // secure.test presents a leaf of a CA the engine trusts; forged.test one of
  // a CA it does not. Both are reached the same way, through the upstream.
  const origins: Record<string, { port: number; got: string[] }> = {};
  for (const [name, caName] of [["secure.test", "trusted-ca"], ["forged.test", "other-ca"], ["raw.test", "trusted-ca"]]) {
    ensureCA(join(work, caName));
    const leaf = await leafFor(join(work, caName), name);
    const got: string[] = [];
    const origin = tlsMod.createServer({ cert: leaf.cert, key: leaf.key }, (s) => {
      s.on("error", () => {});
      s.once("data", (d) => { got.push(d.toString("latin1").split("\r\n")[0]); if (name === "raw.test") { s.end("raw-echo:" + d.toString("latin1")); return; } s.end("HTTP/1.1 200 OK\r\ncontent-length: 6\r\nconnection: close\r\n\r\norigin"); });
    });
    origin.on("tlsClientError", () => {});
    origins[name] = { port: await listen(origin), got };
    cleanups.push(() => origin.close());
  }
  const up = await fakeUpstream({ "secure.test:443": origins["secure.test"].port, "forged.test:443": origins["forged.test"].port, "raw.test:443": origins["raw.test"].port });
  const trapHits: string[] = [];
  const trap = net.createServer((c) => { c.on("error", () => {}); c.once("data", (d) => { trapHits.push(d.toString("latin1").split("\r\n")[0]); c.destroy(); }); });
  const trapPort = await listen(trap);
  cleanups.push(() => trap.close());

  const harness = join(work, "harness.ts");
  writeFileSync(harness, `
import * as net from "node:net";
import * as tls from "node:tls";
import { readFileSync } from "node:fs";
import { start } from ${JSON.stringify(join(import.meta.dir, "engine.ts"))};
const work = ${JSON.stringify(work)};
await start({ socket: work + "/e.sock", caDir: work + "/engine-ca", allow: ["*"], chain: [{ tls: "terminate" }, { tls: "originate" }], upstream: ${JSON.stringify(`http://127.0.0.1:${up.port}`)} });
const get = (host, payload) => new Promise((resolve) => {
  const c = net.connect(work + "/e.sock", () => c.write("CONNECT " + host + ":443 HTTP/1.1\\r\\nHost: " + host + ":443\\r\\n\\r\\n"));
  c.once("data", () => {
    const t = tls.connect({ socket: c, servername: host, ca: readFileSync(work + "/engine-ca/ca.crt") }, () => t.write(payload ?? ("GET /s HTTP/1.1\\r\\nHost: " + host + "\\r\\n\\r\\n")));
    let buf = ""; t.on("data", (d) => { buf += d; }); t.on("end", () => resolve(buf)); t.on("close", () => resolve(buf)); t.on("error", (e) => resolve("error " + e.message));
  });
});
const proxyEnvLeft = ["HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"].filter((k) => process.env[k] !== undefined);
console.log(JSON.stringify({ secure: await get("secure.test"), forged: await get("forged.test"), raw: await get("raw.test", "not http at all"), forgedRaw: await get("forged.test", "raw to a forged origin"), proxyEnvLeft }));
process.exit(0);
`);
  const child = Bun.spawn([process.execPath, harness], {
    env: { ...process.env, NODE_EXTRA_CA_CERTS: join(work, "trusted-ca", "ca.crt"), HTTP_PROXY: `http://127.0.0.1:${trapPort}`, HTTPS_PROXY: `http://127.0.0.1:${trapPort}`, http_proxy: `http://127.0.0.1:${trapPort}`, https_proxy: `http://127.0.0.1:${trapPort}`, NO_PROXY: "", no_proxy: "" },
    stdout: "pipe", stderr: "pipe",
  });
  const out = await new Response(child.stdout).text();
  await child.exited;
  const answers = JSON.parse(out.trim().split("\n").pop() ?? "{}") as { secure: string; forged: string; raw: string; forgedRaw: string; proxyEnvLeft: string[] };
  expect(answers.secure).toStartWith("HTTP/1.1 200");
  expect(answers.secure).toContain("origin");
  expect(answers.forged).toStartWith("HTTP/1.1 502");
  expect(answers.raw).toBe("raw-echo:not http at all");
  // The raw path closes the box's stream when the origin's certificate fails.
  expect(answers.forgedRaw).not.toContain("origin");
  expect(answers.forgedRaw).not.toContain("raw to a forged origin");
  expect(up.seen).toEqual(["CONNECT secure.test:443 HTTP/1.1", "CONNECT forged.test:443 HTTP/1.1", "CONNECT raw.test:443 HTTP/1.1", "CONNECT forged.test:443 HTTP/1.1"]);
  expect(origins["secure.test"].got).toEqual(["GET /s HTTP/1.1"]);
  expect(origins["forged.test"].got).toEqual([]);
  expect(answers.proxyEnvLeft).toEqual([]);
  expect(trapHits).toEqual([]);
});
