// The address check: the engine resolves every name once, refuses local and
// private addresses, and dials the address it checked. Most names here are
// `.test` names the engine looks up through a resolver the test supplies, so
// a passing test cannot have reached anything by a lookup of its own.

import { test, expect, afterEach } from "bun:test";
import { mkdtempSync, mkdirSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import * as net from "node:net";
import * as tls from "node:tls";
import { start, refusedAddress, parseIP, parsePrefix, literalAddress, parseAllowLocal, hostNetworks, REFUSED_HEADER } from "./engine.ts";

const cleanups: (() => void)[] = [];
afterEach(() => { while (cleanups.length) cleanups.pop()!(); });

const ip = (s: string) => { const p = parseIP(s); if (!p) throw new Error(`test fixture ${s} is not an IP`); return p; };

// --- the table ----------------------------------------------------------------

// One address inside each refused range, with the reason it must give. The
// reason is matched exactly, so a deleted row cannot hide behind a wider row
// that happens to cover the same address.
const REFUSED: [string, string][] = [
  ["0.1.2.3", "an address of this network (0.0.0.0/8)"],
  ["0.0.0.0", "an address of this network (0.0.0.0/8)"],
  ["10.20.30.40", "a private address (10.0.0.0/8)"],
  ["100.64.0.1", "a shared address (100.64.0.0/10, carrier NAT and tailnets)"],
  ["100.127.255.254", "a shared address (100.64.0.0/10, carrier NAT and tailnets)"],
  ["127.0.0.1", "a loopback address"],
  ["127.255.0.9", "a loopback address"],
  ["169.254.169.254", "a link-local address (169.254.0.0/16, cloud metadata among them)"],
  ["172.16.0.1", "a private address (172.16.0.0/12)"],
  ["172.31.255.255", "a private address (172.16.0.0/12)"],
  ["192.0.0.8", "an IETF protocol address (192.0.0.0/24)"],
  ["192.168.1.1", "a private address (192.168.0.0/16)"],
  ["198.18.0.1", "a benchmarking address (198.18.0.0/15)"],
  ["198.19.255.1", "a benchmarking address (198.18.0.0/15)"],
  ["224.0.0.251", "a multicast address"],
  ["239.255.255.250", "a multicast address"],
  ["240.0.0.1", "a reserved address (240.0.0.0/4, broadcast among them)"],
  ["255.255.255.255", "a reserved address (240.0.0.0/4, broadcast among them)"],
  ["::", "the unspecified address"],
  ["::1", "a loopback address"],
  ["::7f00:1", "an IPv4-compatible address (::/96)"],
  ["::808:808", "an IPv4-compatible address (::/96)"],
  ["::ffff:0:7f00:1", "an IPv4-translated address (::ffff:0:0:0/96)"],
  ["::ffff:0:808:808", "an IPv4-translated address (::ffff:0:0:0/96)"],
  ["64:ff9b:1::8.8.8.8", "a local-use NAT64 address (64:ff9b:1::/48)"],
  ["100::1", "a discard address (100::/64)"],
  ["2001:0:4136:e378:8000:63bf:3fff:fdd2", "a Teredo address (2001::/32)"],
  ["fc00::1", "a unique local address (fc00::/7)"],
  ["fd12:3456::1", "a unique local address (fc00::/7)"],
  ["fe80::1", "a link-local address"],
  ["febf::1", "a link-local address"],
  ["fec0::1", "a site-local address (fec0::/10)"],
  ["ff02::1", "a multicast address"],
  // IPv4-mapped is checked as the IPv4 address, in either spelling.
  ["::ffff:127.0.0.1", "a loopback address"],
  ["::ffff:7f00:1", "a loopback address"],
  ["::ffff:a9fe:a9fe", "a link-local address (169.254.0.0/16, cloud metadata among them)"],
  ["::ffff:10.0.0.1", "a private address (10.0.0.0/8)"],
  // NAT64 and 6to4 are checked as the address inside them.
  ["64:ff9b::7f00:1", "a NAT64 address of 127.0.0.1, a loopback address"],
  ["64:ff9b::192.168.1.1", "a NAT64 address of 192.168.1.1, a private address (192.168.0.0/16)"],
  ["2002:7f00:1::1", "a 6to4 address of 127.0.0.1, a loopback address"],
  ["2002:a9fe:a9fe::", "a 6to4 address of 169.254.169.254, a link-local address (169.254.0.0/16, cloud metadata among them)"],
];

// Addresses next to each range, or carrying a public IPv4, that must pass.
const ALLOWED = ["1.1.1.1", "8.8.8.8", "9.255.255.255", "11.0.0.0", "100.63.255.255", "100.128.0.0", "126.255.255.255", "128.0.0.0",
  "169.253.255.255", "169.255.0.1", "172.15.255.255", "172.32.0.0", "192.0.1.0", "192.0.2.1", "192.167.255.255", "192.169.0.0",
  "198.17.255.255", "198.20.0.0", "223.255.255.255", "2001:4860:4860::8888", "2001:db8::1", "2001:1::1", "::ffff:8.8.8.8",
  "::ffff:808:808", "64:ff9b::808:808", "2002:808:808::1", "fe7f::1", "fbff::1", "ff::1", "100:0:0:1::1"];

test("refusedAddress: every refused range, with its reason", () => {
  for (const [addr, why] of REFUSED) expect([addr, refusedAddress(ip(addr), [])]).toEqual([addr, why]);
});

test("refusedAddress: the neighbours of each range pass", () => {
  for (const addr of ALLOWED) expect([addr, refusedAddress(ip(addr), [])]).toEqual([addr, ""]);
});

test("refusedAddress: an address on a network of this host's interfaces is refused", () => {
  const networks = [parsePrefix("203.0.113.7/24")!, parsePrefix("2001:db8:1::5/64")!];
  expect(refusedAddress(ip("203.0.113.200"), networks)).toBe("on 203.0.113.7/24, a network of this host's interfaces");
  expect(refusedAddress(ip("::ffff:203.0.113.200"), networks)).toBe("on 203.0.113.7/24, a network of this host's interfaces");
  expect(refusedAddress(ip("2001:db8:1::abcd"), networks)).toBe("on 2001:db8:1::5/64, a network of this host's interfaces");
  // The same addresses pass when no interface is on their network.
  expect(refusedAddress(ip("203.0.114.1"), networks)).toBe("");
  expect(refusedAddress(ip("2001:db8:2::abcd"), networks)).toBe("");
  // Interfaces that cannot be read refuse everything.
  expect(refusedAddress(ip("8.8.8.8"), null)).toBe("unknown: this host's interfaces could not be read");
});

test("hostNetworks: the default reads this host's interfaces", () => {
  const nets = hostNetworks();
  expect(nets).not.toBeNull();
  // Every host has loopback; its network is what the interfaces report.
  expect(nets!.some((n) => n.ip.join(".") === "127.0.0.1" && n.bits === 8)).toBe(true);
});

test("literalAddress: every IP spelling the URL parser accepts, and no name", () => {
  const cases: [string, string | null][] = [
    ["127.0.0.1", "127.0.0.1"], ["127.1", "127.0.0.1"], ["2130706433", "127.0.0.1"], ["0x7f.0.0.1", "127.0.0.1"],
    ["0177.0.0.1", "127.0.0.1"], ["0", "0.0.0.0"], ["[::1]", "::1"], ["::ffff:7f00:1", "::ffff:7f00:1"],
    ["[::ffff:7f00:1]", "::ffff:7f00:1"], ["fe80::1%eth0", "fe80::1"],
    ["localhost", null], ["example.com", null], ["1.2.3.example", null], ["[not-v6]", null],
  ];
  for (const [host, want] of cases) expect([host, literalAddress(host)]).toEqual([host, want]);
});

test("parseAllowLocal: addresses and prefixes, as a list or one string; anything else stops the boot", async () => {
  expect(parseAllowLocal(undefined)).toEqual([]);
  expect(parseAllowLocal(" ")).toEqual([]);
  expect(parseAllowLocal("127.0.0.1, 10.0.0.0/8").map((p) => [p.ip.join("."), p.bits])).toEqual([["127.0.0.1", 32], ["10.0.0.0", 8]]);
  expect(parseAllowLocal(["::1"]).map((p) => p.bits)).toEqual([128]);
  expect(() => parseAllowLocal("localhost")).toThrow(`"localhost" is not an IP address or CIDR prefix`);
  expect(() => parseAllowLocal("10.0.0.0/33")).toThrow("is not an IP address or CIDR prefix");
  const work = mkdtempSync(join(tmpdir(), "dabs-address-"));
  await expect(start({ socket: join(work, "e.sock"), caDir: join(work, "ca"), chain: [], allowLocal: ["10.0.0.0/8/1"] })).rejects.toThrow("is not an IP address or CIDR prefix");
  expect(existsSync(join(work, "e.sock"))).toBe(false);
});

// --- the engine ---------------------------------------------------------------

function listen(server: net.Server): Promise<number> {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve((server.address() as net.AddressInfo).port)));
}

// origin is a TCP server on loopback that counts the connections it gets and
// answers each first chunk with "echo:" and the chunk.
async function origin() {
  const got: string[] = [];
  let connections = 0;
  const server = net.createServer((c) => {
    connections++;
    c.on("error", () => {});
    c.on("data", (d) => { got.push(d.toString("latin1")); c.write("echo:" + d.toString("latin1")); });
  });
  const port = await listen(server);
  cleanups.push(() => server.close());
  return { port, got, connections: () => connections };
}

// NAMES is what the test resolver answers. ok.test is the anchor: opted in
// with allowLocal, it reaches the origin, so a refusal of the others is about
// their address and not a dead engine.
const NAMES: Record<string, string[]> = {
  "ok.test": ["127.0.0.1"],
  "lan.test": ["192.168.1.10"],
  "metadata.test": ["169.254.169.254"],
  "loop6.test": ["::1"],
  "loop2.test": ["127.0.0.2"],
  "mixed.test": ["127.0.0.1", "10.0.0.5"],
  // The engine caches a leaf per host name for the life of the process, signed
  // by the CA of the engine that minted it, so each test that terminates TLS
  // uses names of its own.
  "tlsok.test": ["127.0.0.1"],
  "tlslan.test": ["192.168.1.10"],
  "rawok.test": ["127.0.0.1"],
  "rawlan.test": ["192.168.1.10"],
};

async function engineWith(cfg: { allowLocal?: string[]; resolve?: (h: string) => Promise<string[]>; chain?: object[] } = {}) {
  const work = mkdtempSync(join(tmpdir(), "dabs-address-"));
  const socket = join(work, "engine.sock");
  const caDir = join(work, "ca");
  const log = join(work, "connect.jsonl");
  const lookups: string[] = [];
  const resolve = cfg.resolve ?? (async (h: string) => { lookups.push(h); if (!NAMES[h]) throw new Error(`no test name ${h}`); return NAMES[h]; });
  process.env.DABS_CONNECT_LOG = log;
  const realWarn = console.warn;
  console.warn = () => {};
  let engine;
  try {
    engine = await start({ socket, caDir, allow: ["*"], chain: (cfg.chain ?? []) as never, resolve, ...("allowLocal" in cfg ? { allowLocal: cfg.allowLocal } : {}) });
  } finally { console.warn = realWarn; delete process.env.DABS_CONNECT_LOG; }
  cleanups.push(() => engine.stop());
  const ledger = () => (existsSync(log) ? readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : []);
  return { socket, caDir, ledger, lookups };
}

// connectThrough plays the box: CONNECT, and on a 200 send one chunk and wait
// for one answer. It resolves with the whole response head and the answer.
function connectThrough(socket: string, authority: string, payload = "ping"): Promise<{ head: string; reply: string }> {
  return new Promise((resolve) => {
    const c = net.connect(socket);
    let buf = "";
    let head = "";
    c.on("error", () => {});
    c.on("connect", () => c.write(`CONNECT ${authority} HTTP/1.1\r\nHost: ${authority}\r\n\r\n`));
    c.on("data", (d) => {
      buf += d.toString("latin1");
      if (!head) {
        const end = buf.indexOf("\r\n\r\n");
        if (end < 0) return;
        head = buf.slice(0, end);
        buf = buf.slice(end + 4);
        if (!/ 200 /.test(head.split("\r\n")[0])) return;
        c.write(payload);
      }
      if (buf && / 200 /.test(head)) { resolve({ head, reply: buf }); c.destroy(); }
    });
    c.on("close", () => resolve({ head: head || "(closed)", reply: buf }));
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

// throughWindow plays the box through a terminate window: CONNECT, a TLS
// handshake trusting the engine's CA, then payload. It resolves with what
// came back over TLS, after "secure:" once the handshake with the engine
// completed, so a stream the engine closed ("secure:") is not mistaken for a
// handshake that failed ("(no handshake)").
function throughWindow(socket: string, caDir: string, host: string, port: number, payload: string): Promise<string> {
  return new Promise((resolve) => {
    const c = net.connect(socket, () => c.write(`CONNECT ${host}:${port} HTTP/1.1\r\nHost: ${host}:${port}\r\n\r\n`));
    c.on("error", () => resolve("(error)"));
    c.once("data", (d) => {
      if (!d.toString("latin1").startsWith("HTTP/1.1 200")) { resolve("CONNECT " + d.toString("latin1").split("\r\n")[0]); return; }
      let buf = "(no handshake)";
      const t = tls.connect({ socket: c, servername: host, ca: readFileSync(join(caDir, "ca.crt")), ALPNProtocols: ["http/1.1"] }, () => { buf = "secure:"; t.write(payload); });
      t.on("data", (x) => { buf += x.toString("latin1"); });
      t.on("error", () => {});
      t.on("close", () => resolve(buf));
    });
  });
}

const refusedHeader = (response: string) => response.split("\r\n\r\n")[0].split("\r\n").find((l) => l.startsWith(REFUSED_HEADER + ": "))?.slice(REFUSED_HEADER.length + 2);

test("CONNECT tunnel: a name that resolves to a refused address gets 403 with the reason, before any tunnel", async () => {
  const o = await origin();
  const { socket, ledger } = await engineWith({ allowLocal: ["127.0.0.1"] });

  const ok = await connectThrough(socket, `ok.test:${o.port}`);
  expect(ok.reply).toBe("echo:ping");

  for (const [name, addr, why] of [["lan.test", "192.168.1.10", "a private address (192.168.0.0/16)"], ["metadata.test", "169.254.169.254", "a link-local address (169.254.0.0/16, cloud metadata among them)"], ["loop6.test", "::1", "a loopback address"], ["mixed.test", "10.0.0.5", "a private address (10.0.0.0/8)"]]) {
    const r = await connectThrough(socket, `${name}:${o.port}`);
    expect([name, r.head.split("\r\n")[0]]).toEqual([name, "HTTP/1.1 403 Forbidden"]);
    expect([name, refusedHeader(r.head)]).toEqual([name, `${name} is ${addr}, ${why}; egress never reaches this host, its networks or a private range`]);
  }
  // mixed.test's first address is the origin's, and it was still not dialed.
  expect(o.connections()).toBe(1);
  expect(o.got).toEqual(["ping"]);
  const denied = ledger().filter((e) => e.verdict === "deny-address");
  expect(denied.map((e) => [e.host, e.address, e.proto])).toEqual([["lan.test", "192.168.1.10", "connect"], ["metadata.test", "169.254.169.254", "connect"], ["loop6.test", "::1", "connect"], ["mixed.test", "10.0.0.5", "connect"]]);
});

test("plain HTTP forward: a name that resolves to a refused address gets 403 with the reason", async () => {
  const o = await origin();
  const { socket, ledger } = await engineWith({ allowLocal: ["127.0.0.1"] });

  const ok = await plainGet(socket, `http://ok.test:${o.port}/`);
  expect(o.connections()).toBe(1);
  expect(o.got[0]).toStartWith("GET / HTTP/1.1");
  expect(ok).not.toContain(REFUSED_HEADER);

  const r = await plainGet(socket, `http://lan.test:${o.port}/`);
  expect(r).toStartWith("HTTP/1.1 403 Forbidden");
  expect(refusedHeader(r)).toBe("lan.test is 192.168.1.10, a private address (192.168.0.0/16); egress never reaches this host, its networks or a private range");
  expect(o.connections()).toBe(1);
  expect(ledger().filter((e) => e.verdict === "deny-address").map((e) => [e.host, e.proto])).toEqual([["lan.test", "http"]]);
});

test("terminated TLS: the re-originated HTTP request to a refused address is answered 403 inside the tunnel", async () => {
  const o = await origin();
  const { socket, caDir, ledger } = await engineWith({ allowLocal: ["127.0.0.1"], chain: [{ tls: "terminate" }, { tls: "originate" }] });

  // The anchor: ok.test is dialed. The origin does not speak TLS, so the
  // engine's handshake to it fails and the box gets a 502, after the origin
  // saw the connection.
  const ok = await throughWindow(socket, caDir, "tlsok.test", o.port, "GET / HTTP/1.1\r\nHost: tlsok.test\r\n\r\n");
  expect(ok).toStartWith("secure:HTTP/1.1 502");
  expect(o.connections()).toBe(1);

  const r = await throughWindow(socket, caDir, "tlslan.test", o.port, "GET / HTTP/1.1\r\nHost: tlslan.test\r\n\r\n");
  expect(r).toStartWith("secure:HTTP/1.1 403 Forbidden");
  expect(refusedHeader(r.slice("secure:".length))).toBe("tlslan.test is 192.168.1.10, a private address (192.168.0.0/16); egress never reaches this host, its networks or a private range");
  expect(o.connections()).toBe(1);
  expect(ledger().filter((e) => e.verdict === "deny-address").map((e) => [e.host, e.address])).toEqual([["tlslan.test", "192.168.1.10"]]);
});

test("terminated TLS: a raw stream to a refused address is closed and recorded", async () => {
  const o = await origin();
  const { socket, caDir, ledger } = await engineWith({ allowLocal: ["127.0.0.1"], chain: [{ tls: "terminate" }, { tls: "originate" }] });

  // The anchor: a non-HTTP stream to ok.test is re-originated, so the origin
  // sees a connection (the TLS it is sent is not its protocol).
  await throughWindow(socket, caDir, "rawok.test", o.port, "not http at all");
  expect(o.connections()).toBe(1);

  const r = await throughWindow(socket, caDir, "rawlan.test", o.port, "not http at all");
  expect(r).toBe("secure:");
  expect(o.connections()).toBe(1);
  expect(ledger().filter((e) => e.verdict === "deny-address").map((e) => [e.host, e.address, e.proto])).toEqual([["rawlan.test", "192.168.1.10", "connect"]]);
});

test("the system resolver: localhost is refused on every path, with no opt-in", async () => {
  const o = await origin();
  const work = mkdtempSync(join(tmpdir(), "dabs-address-"));
  const log = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = log;
  let engine;
  try { engine = await start({ socket: join(work, "e.sock"), caDir: join(work, "ca"), allow: ["*"], chain: [], allowLocal: [] }); }
  finally { delete process.env.DABS_CONNECT_LOG; }
  cleanups.push(() => engine.stop());
  const socket = join(work, "e.sock");

  const r = await connectThrough(socket, `localhost:${o.port}`);
  expect(r.head.split("\r\n")[0]).toBe("HTTP/1.1 403 Forbidden");
  expect(refusedHeader(r.head)).toMatch(/^localhost is (::1|127\.0\.0\.1), a loopback address;/);
  expect(await plainGet(socket, `http://localhost:${o.port}/`)).toStartWith("HTTP/1.1 403 Forbidden");
  expect(o.connections()).toBe(0);
  expect(readFileSync(log, "utf8").trim().split("\n").map((l) => JSON.parse(l)).filter((e) => e.verdict === "deny-address").map((e) => e.proto)).toEqual(["connect", "http"]);
});

test("IP literals in every spelling are refused on both proxy paths, with no lookup", async () => {
  const o = await origin();
  const { socket, lookups } = await engineWith({ allowLocal: [] });
  for (const authority of [`127.0.0.1:${o.port}`, `127.1:${o.port}`, `2130706433:${o.port}`, `0x7f.0.0.1:${o.port}`, `[::1]:${o.port}`,
    `[::ffff:7f00:1]:${o.port}`, `[::ffff:127.0.0.1]:${o.port}`, `[::7f00:1]:${o.port}`, `[::ffff:0:7f00:1]:${o.port}`, `[64:ff9b::7f00:1]:${o.port}`,
    `[2002:7f00:1::1]:${o.port}`, `[64:ff9b:1::1]:${o.port}`, `[fe80::1]:${o.port}`, `0.0.0.0:${o.port}`, `10.0.0.1:${o.port}`]) {
    const r = await connectThrough(socket, authority);
    expect([authority, r.head.split("\r\n")[0]]).toEqual([authority, "HTTP/1.1 403 Forbidden"]);
  }
  for (const url of [`http://[::ffff:7f00:1]:${o.port}/`, `http://127.1:${o.port}/`, `http://[::1]:${o.port}/`]) {
    expect([url, (await plainGet(socket, url)).split("\r\n")[0]]).toEqual([url, "HTTP/1.1 403 Forbidden"]);
  }
  expect(o.connections()).toBe(0);
  expect(lookups).toEqual([]);
});

test("the engine dials the address it checked: one lookup per connection, never the name again", async () => {
  const o = await origin();
  // flip.test answers the origin's address once, then a refused one. Only the
  // test resolver knows the name, so a dial by name could not reach anything.
  let calls = 0;
  const resolve = async (h: string) => { if (h !== "flip.test") throw new Error("unknown"); calls++; return calls % 2 === 1 ? ["127.0.0.1"] : ["10.9.9.9"]; };
  const { socket } = await engineWith({ allowLocal: ["127.0.0.1"], resolve });

  const r = await connectThrough(socket, `flip.test:${o.port}`);
  expect(r.reply).toBe("echo:ping");
  expect(calls).toBe(1);
  expect(await plainGet(socket, `http://flip.test:${o.port}/`)).toStartWith("HTTP/1.1 403 Forbidden");
  expect(calls).toBe(2);
  expect(o.connections()).toBe(1);
});

test("DABS_EGRESS_ALLOW_LOCAL opens the addresses it names when the config names none", async () => {
  const o = await origin();
  process.env.DABS_EGRESS_ALLOW_LOCAL = "127.0.0.1";
  let fromEnv, overridden;
  try {
    fromEnv = await engineWith({});
    overridden = await engineWith({ allowLocal: [] });
  } finally { delete process.env.DABS_EGRESS_ALLOW_LOCAL; }
  expect((await connectThrough(fromEnv.socket, `ok.test:${o.port}`)).reply).toBe("echo:ping");
  expect((await connectThrough(overridden.socket, `ok.test:${o.port}`)).head.split("\r\n")[0]).toBe("HTTP/1.1 403 Forbidden");
  // An opt-in for one address opens nothing next to it.
  for (const name of ["loop2.test", "loop6.test"]) expect([name, (await connectThrough(fromEnv.socket, `${name}:${o.port}`)).head.split("\r\n")[0]]).toEqual([name, "HTTP/1.1 403 Forbidden"]);
  expect(o.connections()).toBe(1);
});

test("an opt-in is announced at boot", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-address-"));
  const warnings: string[] = [];
  const realWarn = console.warn;
  console.warn = (...a: unknown[]) => { warnings.push(a.map(String).join(" ")); };
  let engine;
  try { engine = await start({ socket: join(work, "e.sock"), caDir: join(work, "ca"), chain: [], allowLocal: ["127.0.0.1", "10.0.0.0/8"] }); }
  finally { console.warn = realWarn; }
  cleanups.push(() => engine.stop());
  expect(warnings).toContain("warning: egress may reach these local or private addresses: 127.0.0.1/32, 10.0.0.0/8");
});

test("an allowed IPv6 literal is dialed bare and written back in brackets", async () => {
  const got: string[] = [];
  const server = net.createServer((c) => {
    c.on("error", () => {});
    c.once("data", (d) => { got.push(d.toString("latin1").split("\r\n").find((l) => l.toLowerCase().startsWith("host:")) ?? d.toString("latin1")); c.end("HTTP/1.1 200 OK\r\ncontent-length: 2\r\nconnection: close\r\n\r\nv6"); });
  });
  const port = await new Promise<number>((resolve) => server.listen(0, "::1", () => resolve((server.address() as net.AddressInfo).port)));
  cleanups.push(() => server.close());
  const { socket, ledger } = await engineWith({ allowLocal: ["::1"] });

  const r = await connectThrough(socket, `[::1]:${port}`, "GET / HTTP/1.1\r\nHost: tunnel\r\n\r\n");
  expect(r.head.split("\r\n")[0]).toBe("HTTP/1.1 200 Connection Established");
  expect(await plainGet(socket, `http://[::1]:${port}/`)).toContain("v6");
  expect(got).toEqual(["Host: tunnel", `host: [::1]:${port}`]);
  expect(ledger().filter((e) => e.t === "connect").map((e) => [e.host, e.verdict])).toEqual([["::1", "tunnel"], ["::1", "forward"]]);
});

test("fail-open without a terminator: the raw tunnel to a refused address is closed and recorded", async () => {
  const o = await origin();
  const work = mkdtempSync(join(tmpdir(), "dabs-address-"));
  const caDir = join(work, "ca");
  const log = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = log;
  const realWarn = console.warn;
  console.warn = () => {};
  let engine;
  try {
    engine = await start({ socket: join(work, "e.sock"), caDir, allow: ["*"], chain: [{ tls: "terminate", failOpen: true }, { tls: "originate" }], allowLocal: ["127.0.0.1"], resolve: async (h) => NAMES[h] ?? [] });
  } finally { console.warn = realWarn; delete process.env.DABS_CONNECT_LOG; }
  cleanups.push(() => engine.stop());
  // A directory where each terminator would bind its socket (makeTerminator
  // names it after a hash of host:port), so no terminator can listen and
  // fail_open tunnels raw instead.
  for (const host of ["ok.test", "lan.test"]) mkdirSync(join(caDir, `t${Bun.hash(`${host}:${o.port}`).toString(36).slice(0, 12)}.sock`));
  const socket = join(work, "e.sock");

  const ok = await connectThrough(socket, `ok.test:${o.port}`, "\x16hello");
  expect(ok.reply).toBe("echo:\x16hello");
  const r = await connectThrough(socket, `lan.test:${o.port}`, "\x16hello");
  expect(r.head.split("\r\n")[0]).toBe("HTTP/1.1 200 Connection Established");
  expect(r.reply).toBe("");
  expect(o.connections()).toBe(1);
  const lines = readFileSync(log, "utf8").trim().split("\n").map((l) => JSON.parse(l)).filter((e) => e.host === "lan.test");
  expect(lines.map((e) => e.verdict)).toEqual(["tunnel-noterminator", "deny-address"]);
});
