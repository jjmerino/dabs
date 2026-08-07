// Standalone proof of the proxy engine, no dabs box required. It stands up the
// engine over a two-hop peek window — an inner recorder and an outer responder —
// then acts as the box: CONNECT to the engine socket, TLS-handshake trusting the
// engine's CA, GET https://dabs.dev/fake/hello. Asserts the responder ANSWERED
// (so nothing left for the real internet) and the recorder WROTE the exchange.

import { test, expect, beforeAll, afterAll } from "bun:test";
import { mkdtempSync, writeFileSync, readdirSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import * as net from "node:net";
import * as tls from "node:tls";
import { start } from "./engine.ts";

const work = mkdtempSync(join(tmpdir(), "dabs-proxy-"));
const caDir = join(work, "ca");
const cassettes = join(work, "cassettes");
const socket = join(work, "engine.sock");
let engine: { stop: () => void };

// The two custom proxy modules the "test writes its own" story calls for: each
// is a factory (config) => handler over the engine contract.
const recorderMod = join(work, "recorder.ts");
const responderMod = join(work, "responder.ts");

// The recorder taps: it observes the request head and accumulates the response
// body across onResponseChunk (never buffering in the engine — the hook owns its
// own accumulation) so it can log the full body once, at EOF.
writeFileSync(
  recorderMod,
  `import { appendFileSync, mkdirSync } from "node:fs";
export default (config) => {
  mkdirSync(config.dir, { recursive: true });
  return {
    onRequest(head) {
      appendFileSync(config.dir + "/log.jsonl", JSON.stringify({ kind: "request", path: head.path, host: head.host }) + "\\n");
    },
    onResponse(head, ctx) { ctx.status = head.status; ctx.body = ""; },
    onResponseChunk(chunk, ctx) {
      if (chunk === null) {
        appendFileSync(config.dir + "/log.jsonl", JSON.stringify({ kind: "response", status: ctx.status, body: ctx.body }) + "\\n");
        return;
      }
      ctx.body += chunk.toString("latin1"); // tap: observe, pass through unchanged
    },
  };
};
`,
);

writeFileSync(
  responderMod,
  `export default () => ({
  onRequest(head) {
    if (head.path === "/fake/hello") {
      return { action: "respond", status: 200, body: "hello from the fake dabs.dev" };
    }
    return { action: "respond", status: 404, body: "no" };
  },
});
`,
);

beforeAll(async () => {
  engine = await start({
    socket,
    caDir,
    chain: [
      { tls: "terminate" },
      { name: "recorder", module: recorderMod, config: { dir: cassettes } },
      { name: "responder", module: responderMod, config: {} },
      { tls: "originate" }, // closes the window; the responder answers first so this never forwards
    ],
  });
});

afterAll(() => engine?.stop());

// Drive one HTTPS request the way the box would: open a raw connection to the
// engine's proxy socket, send CONNECT, then run TLS over that tunnel trusting
// the engine's CA (as the box's trust store would), and speak HTTP/1.1.
// dechunk decodes an HTTP/1.1 chunked-transfer body (latin1 string form).
function dechunk(s: string): string {
  let out = "", i = 0;
  for (;;) {
    const nl = s.indexOf("\r\n", i);
    if (nl < 0) break;
    const size = parseInt(s.slice(i, nl).trim().split(";")[0], 16);
    if (!size) break;
    out += s.slice(nl + 2, nl + 2 + size);
    i = nl + 2 + size + 2;
  }
  return out;
}

function httpsThroughProxy(host: string, path: string): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const ca = readFileSync(join(caDir, "ca.crt"));
    const raw = net.connect(socket, () => {
      raw.write(`CONNECT ${host}:443 HTTP/1.1\r\nHost: ${host}:443\r\n\r\n`);
    });
    let established = false;
    raw.once("data", (buf) => {
      if (!buf.toString("latin1").startsWith("HTTP/1.1 200")) {
        reject(new Error("CONNECT failed: " + buf.toString("latin1").split("\r\n")[0]));
        return;
      }
      established = true;
      const secure = tls.connect({ socket: raw, servername: host, ca }, () => {
        secure.write(`GET ${path} HTTP/1.1\r\nHost: ${host}\r\nConnection: close\r\n\r\n`);
      });
      let data = "";
      secure.on("data", (d) => (data += d.toString("latin1")));
      secure.on("end", () => {
        const head = data.split("\r\n\r\n")[0];
        const raw = data.slice(data.indexOf("\r\n\r\n") + 4);
        const status = Number(head.split("\r\n")[0].split(" ")[1]);
        // The engine streams responses with Transfer-Encoding: chunked; decode it.
        const chunked = /transfer-encoding:\s*chunked/i.test(head);
        resolve({ status, body: chunked ? dechunk(raw) : raw });
      });
      secure.on("error", reject);
    });
    raw.on("error", (e) => { if (!established) reject(e); });
  });
}

test("outer proxy responds terminally; inner proxy records the exchange", async () => {
  const res = await httpsThroughProxy("dabs.dev", "/fake/hello");

  // The mock answered — nothing went to the real dabs.dev.
  expect(res.status).toBe(200);
  expect(res.body).toBe("hello from the fake dabs.dev");

  // The recorder wrote both the request and the response it turned around.
  const logPath = join(cassettes, "log.jsonl");
  expect(existsSync(logPath)).toBe(true);
  const lines = readFileSync(logPath, "utf8").trim().split("\n").map((l) => JSON.parse(l));
  const reqRec = lines.find((l) => l.kind === "request");
  const resRec = lines.find((l) => l.kind === "response");
  expect(reqRec?.path).toBe("/fake/hello");
  expect(reqRec?.host).toBe("dabs.dev");
  expect(resRec?.status).toBe(200);
  expect(resRec?.body).toBe("hello from the fake dabs.dev");
});

// --- policy + streaming forward, over the plain-HTTP forward-proxy path. -----
// A real local upstream (node http) proves the streaming forward: the engine
// pumps the request body up through onRequestChunk and streams the response back
// through onResponseChunk (a rewrite), never buffering. The allow list proves
// the engine-native CONNECT/host gate.

import * as http from "node:http";

// forwardProxy speaks the plain-HTTP forward-proxy shape to the engine socket:
// `METHOD http://host:port/path HTTP/1.1`, streaming an optional body.
function forwardProxy(sock: string, absUrl: string, method = "GET", body = ""): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const c = net.connect(sock, () => {
      const u = new URL(absUrl);
      let head = `${method} ${absUrl} HTTP/1.1\r\nHost: ${u.host}\r\nConnection: close\r\n`;
      if (body) head += `Content-Length: ${Buffer.byteLength(body)}\r\n`;
      c.write(head + "\r\n" + body);
    });
    let data = "";
    c.on("data", (d) => (data += d.toString("latin1")));
    c.on("end", () => {
      const headEnd = data.indexOf("\r\n\r\n");
      const head = data.slice(0, headEnd);
      const raw = data.slice(headEnd + 4);
      const status = Number(head.split("\r\n")[0].split(" ")[1]);
      const b = /transfer-encoding:\s*chunked/i.test(head) ? dechunk(raw) : raw;
      resolve({ status, body: b });
    });
    c.on("error", reject);
  });
}

test("engine gates CONNECT by allow list and streams a forward with chunk rewrites", async () => {
  // A local upstream that echoes the request body and emits a token to rewrite.
  const upstream = http.createServer((req, res) => {
    let b = "";
    req.on("data", (d) => (b += d));
    req.on("end", () => { res.writeHead(200, { "content-type": "text/plain" }); res.end(`got[${b}] token=SECRET-abc`); });
  });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const uport = (upstream.address() as net.AddressInfo).port;

  const work2 = mkdtempSync(join(tmpdir(), "dabs-fwd-"));
  const swapMod = join(work2, "swap.ts");
  // A streaming hook: uppercases the request body chunk-by-chunk (onRequestChunk)
  // and rewrites the token in the response chunk-by-chunk (onResponseChunk) — no
  // buffering; both are pure per-chunk transforms.
  writeFileSync(swapMod, `export default () => ({
    onRequestChunk(chunk) { return chunk === null ? null : Buffer.from(chunk.toString("latin1").toUpperCase()); },
    onResponseChunk(chunk) { return chunk === null ? null : Buffer.from(chunk.toString("latin1").replace("SECRET-abc", "REDACTED")); },
  });`);

  const socket2 = join(work2, "engine.sock");
  const eng2 = await start({
    socket: socket2,
    caDir: join(work2, "ca"),
    allow: ["127.0.0.1"],
    chain: [{ tls: "terminate" }, { name: "swap", module: swapMod, config: {} }, { tls: "originate" }],
  });

  // Allowed host, with a body: request upper-cased upstream, token redacted back.
  const ok = await forwardProxy(socket2, `http://127.0.0.1:${uport}/x`, "POST", "hello");
  expect(ok.status).toBe(200);
  expect(ok.body).toBe("got[HELLO] token=REDACTED");

  // A denied host never reaches an upstream — the engine answers 403 at CONNECT.
  const denied = await new Promise<string>((resolve) => {
    const c = net.connect(socket2, () => c.write("CONNECT evil.example.com:443 HTTP/1.1\r\nHost: evil.example.com:443\r\n\r\n"));
    c.once("data", (d) => { resolve(d.toString("latin1").split("\r\n")[0]); c.destroy(); });
  });
  expect(denied).toContain("403");

  eng2.stop();
  upstream.close();
});

// Regression: the per-host terminator listens on a unix socket in the (long,
// on macOS) temp caDir. A descriptive socket name for a long host overflowed the
// ~104-byte unix path cap, so the engine died at boot — but only for a long host
// (the earlier test's "dabs.dev" fit). A 40+ char host must still terminate.
test("terminates a long hostname without overflowing the unix socket path", async () => {
  const res = await httpsThroughProxy("very-long-subdomain-name.api.anthropic.example", "/fake/hello");
  expect(res.status).toBe(200);
  expect(res.body).toBe("hello from the fake dabs.dev");
});

import { helloHasECH } from "./engine.ts";

// Build a minimal TLS ClientHello carrying the given extension types, to exercise
// the ECH detector without a live TLS stack.
function buildHello(extTypes: number[]): Buffer {
  const u16 = (n: number) => { const b = Buffer.alloc(2); b.writeUInt16BE(n, 0); return b; };
  const u24 = (n: number) => Buffer.from([(n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff]);
  const exts = Buffer.concat(extTypes.map((t) => Buffer.concat([u16(t), u16(0)])));
  const body = Buffer.concat([
    Buffer.from([0x03, 0x03]), Buffer.alloc(32), // version + random
    Buffer.from([0x00]),                          // session_id (len 0)
    Buffer.from([0x00, 0x02, 0x00, 0x2f]),        // cipher_suites (one)
    Buffer.from([0x01, 0x00]),                    // compression (null)
    u16(exts.length), exts,
  ]);
  const hs = Buffer.concat([Buffer.from([0x01]), u24(body.length), body]);
  return Buffer.concat([Buffer.from([0x16, 0x03, 0x01]), u16(hs.length), hs]);
}

import { ensureCA, leafFor } from "./engine.ts";
import { execFileSync } from "node:child_process";

// A hostname longer than 64 chars once broke leaf minting: it was the cert CN,
// which X.509 caps at 64. The CN is now constant and the host lives in the SAN,
// so a long host must still mint a valid cert (openssl exits non-zero otherwise,
// which leafFor now surfaces by throwing).
test("leafFor mints a valid cert for a host longer than 64 chars", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dabs-leaf-"));
  ensureCA(dir);
  const host = "a".repeat(80) + ".example.com"; // 92 chars, well over the CN cap
  const leaf = await leafFor(dir, host);
  expect(leaf.cert).toContain("BEGIN CERTIFICATE");
  // The cert parses and carries the long host in its SAN.
  const files = readdirSync(dir).filter((f) => f.startsWith("leaf-") && f.endsWith(".crt"));
  const text = execFileSync("openssl", ["x509", "-in", join(dir, files[0]), "-noout", "-text"]).toString();
  expect(text).toContain(host);
});

// Two concurrent first-contacts to the same uncached host must share ONE mint
// (single-flight) — otherwise they interleave openssl over a shared path and the
// first connection gets a half-written, SAN-less cert.
test("leafFor single-flights concurrent mints for the same host", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dabs-race-"));
  ensureCA(dir);
  const host = "race.example.com";
  const a = leafFor(dir, host);
  const b = leafFor(dir, host);
  expect(a).toBe(b); // same in-flight Promise, not two mints
  const [ca, cb] = await Promise.all([a, b]);
  expect(ca.cert).toBe(cb.cert);
  expect(ca.cert).toContain("BEGIN CERTIFICATE");
});

test("helloHasECH detects the encrypted_client_hello extension (0xfe0d)", () => {
  expect(helloHasECH(buildHello([0x0000]))).toBe(false);          // just server_name
  expect(helloHasECH(buildHello([0x0000, 0xfe0d]))).toBe(true);   // ECH present
  expect(helloHasECH(buildHello([0xfe0d]))).toBe(true);
  expect(helloHasECH(Buffer.from("not a tls record"))).toBe(false);
  expect(helloHasECH(Buffer.alloc(3))).toBe(false);               // too short
});

test("connect ledger records every dialed destination and its verdict", async () => {
  const upstream = http.createServer((_req, res) => { res.writeHead(200); res.end("ok"); });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const uport = (upstream.address() as net.AddressInfo).port;

  const work = mkdtempSync(join(tmpdir(), "dabs-ledger-"));
  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger; // inherited by the engine (same process here)

  const socket = join(work, "engine.sock");
  const eng = await start({ socket, caDir: join(work, "ca"), allow: ["127.0.0.1"], chain: [] });

  // Allowed plain-HTTP forward → a "forward" line; denied host → a "deny" line.
  await forwardProxy(socket, `http://127.0.0.1:${uport}/x`, "GET", "");
  await new Promise<void>((resolve) => {
    const c = net.connect(socket, () => c.write("CONNECT evil.example.com:443 HTTP/1.1\r\n\r\n"));
    c.once("data", () => { c.destroy(); resolve(); });
  });

  const lines = readFileSync(ledger, "utf8").trim().split("\n").map((l) => JSON.parse(l));
  const forward = lines.find((l) => l.host === "127.0.0.1" && l.verdict === "forward");
  const deny = lines.find((l) => l.host === "evil.example.com" && l.verdict === "deny");
  expect(forward).toBeTruthy();
  expect(deny).toBeTruthy();
  expect(deny.port).toBe(443);

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
  upstream.close();
});

import { matchHost } from "./engine.ts";

// One pattern grammar backs BOTH the CONNECT gate and the terminate scope, so a
// domain never means two things by where it is written. These cases pin the
// grammar — and are the regression net for the bug where a bare domain matched
// only the apex at the gate (denying api.anthropic.com) while a `*.` form
// matched NOTHING in the terminate list (tunnelling the real token to the box).
test("matchHost: a bare domain matches the apex AND every subdomain", () => {
  expect(matchHost("anthropic.com", "anthropic.com")).toBe(true);
  expect(matchHost("anthropic.com", "api.anthropic.com")).toBe(true);
  expect(matchHost("anthropic.com", "console.anthropic.com")).toBe(true);
  expect(matchHost("anthropic.com", "evil-anthropic.com")).toBe(false);
  expect(matchHost("anthropic.com", "anthropic.com.evil.com")).toBe(false);
});

test("matchHost: a `*.` pattern matches subdomains only, never the apex", () => {
  expect(matchHost("*.anthropic.com", "api.anthropic.com")).toBe(true);
  expect(matchHost("*.anthropic.com", "anthropic.com")).toBe(false);
});

test("matchHost: `*` matches everything, case/trailing-dot are caller-canonical", () => {
  expect(matchHost("*", "anything.example.com")).toBe(true);
  expect(matchHost("ANTHROPIC.COM", "api.anthropic.com")).toBe(true); // pattern lowercased
});

// --- the CONNECTION tier: a module hop outside a tls window. -----------------
// A module hop needs no tls window: outside one it acts on the connection
// (host/port) through onConnect. These pin the whole contract — narrowing only,
// both proxy paths, fail closed, chain order, and a distinguishable ledger line.

// connectLine sends one CONNECT to the engine socket and resolves the status
// line it answers with (the box's view of the gate's verdict). It never sends a
// ClientHello, so an ALLOWED connection dials nothing.
function connectLine(sock: string, host: string, port: number): Promise<string> {
  return new Promise((resolve, reject) => {
    const c = net.connect(sock, () => c.write(`CONNECT ${host}:${port} HTTP/1.1\r\nHost: ${host}:${port}\r\n\r\n`));
    c.once("data", (d) => { resolve(d.toString("latin1").split("\r\n")[0]); c.destroy(); });
    c.on("error", reject);
  });
}

// ledgerLines reads the connection ledger written so far.
function ledgerLines(path: string): Record<string, unknown>[] {
  return readFileSync(path, "utf8").trim().split("\n").map((l) => JSON.parse(l));
}

test("onConnect denies a connection the static policy allows, and the ledger says which hop", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-"));
  const gateMod = join(work, "gate.ts");
  writeFileSync(gateMod, `export default (config) => ({
    onConnect(target) {
      if (target.host === config.block) return { action: "deny", reason: "blocked by the gate hook" };
    },
  });`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    allow: ["example.com"], // the apex and every subdomain: BOTH hosts below pass the static gate
    chain: [{ name: "gate", module: gateMod, config: { block: "blocked.example.com" } }],
  });

  expect(await connectLine(socket, "ok.example.com", 443)).toContain("200");
  expect(await connectLine(socket, "blocked.example.com", 443)).toContain("403");

  const lines = ledgerLines(ledger);
  const denial = lines.find((l) => l.host === "blocked.example.com");
  expect(denial?.verdict).toBe("deny-module"); // NOT "deny": a hook refused, not the recipe
  expect(denial?.hop).toBe("gate");
  expect(denial?.reason).toBe("blocked by the gate hook");
  const allowed = lines.find((l) => l.host === "ok.example.com");
  expect(allowed).toBeTruthy(); // the allowed host reached the ledger at all
  expect(allowed?.verdict).not.toBe("deny-module");

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
});

test("onConnect cannot widen the static policy — a statically denied host never reaches the hook", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-widen-"));
  const seen = join(work, "seen.jsonl");
  const yesMod = join(work, "yes.ts");
  writeFileSync(yesMod, `import { appendFileSync } from "node:fs";
export default (config) => ({
  onConnect(target) {
    appendFileSync(config.seen, JSON.stringify(target) + "\\n");
    return { action: "allow" };
  },
});`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    allow: ["good.example.com"],
    chain: [{ name: "yes", module: yesMod, config: { seen } }],
  });

  expect(await connectLine(socket, "evil.example.com", 443)).toContain("403");
  expect(await connectLine(socket, "good.example.com", 443)).toContain("200");

  // The static deny stands, is recorded as the recipe's own, and the hook that
  // would have allowed everything was never asked about that host.
  expect(ledgerLines(ledger).find((l) => l.host === "evil.example.com")?.verdict).toBe("deny");
  const consulted = readFileSync(seen, "utf8").trim().split("\n").map((l) => JSON.parse(l));
  expect(consulted).toEqual([{ host: "good.example.com", port: 443 }]);

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
});

test("the connection tier fails closed: a throwing or hanging onConnect denies", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-closed-"));
  const brokenMod = join(work, "broken.ts");
  writeFileSync(brokenMod, `export default () => ({
    onConnect(target) {
      if (target.host === "throw.example.com") throw new Error("hook blew up");
      if (target.host === "hang.example.com") return new Promise(() => {}); // never settles
      if (target.host === "garbage.example.com") return 42;
    },
  });`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    allow: ["example.com"],
    chain: [{ name: "broken", module: brokenMod, config: {} }],
  });

  expect(await connectLine(socket, "throw.example.com", 443)).toContain("403");
  expect(await connectLine(socket, "hang.example.com", 443)).toContain("403");
  expect(await connectLine(socket, "garbage.example.com", 443)).toContain("403");
  expect(await connectLine(socket, "fine.example.com", 443)).toContain("200");

  const lines = ledgerLines(ledger);
  expect(lines.find((l) => l.host === "throw.example.com")?.reason).toContain("hook blew up");
  expect(lines.find((l) => l.host === "hang.example.com")?.reason).toContain("exceeded");
  expect(lines.find((l) => l.host === "garbage.example.com")?.reason).toContain("not a verdict");

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
}, 10_000);

test("onConnect hops run in chain order — the first deny wins", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-order-"));
  const denyMod = join(work, "deny.ts");
  writeFileSync(denyMod, `export default (config) => ({
    onConnect() { return { action: "deny", reason: config.reason }; },
  });`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    chain: [
      { name: "box-side", module: denyMod, config: { reason: "first" } },
      { name: "internet-side", module: denyMod, config: { reason: "second" } },
    ],
  });

  expect(await connectLine(socket, "anything.example.com", 443)).toContain("403");
  const denial = ledgerLines(ledger).find((l) => l.host === "anything.example.com");
  expect(denial?.hop).toBe("box-side");
  expect(denial?.reason).toBe("first");

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
});

test("the plain forward-proxy path consults onConnect too", async () => {
  const upstream = http.createServer((_req, res) => { res.writeHead(200); res.end("reached the upstream"); });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const uport = (upstream.address() as net.AddressInfo).port;

  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-http-"));
  const portMod = join(work, "port.ts");
  // Deny by PORT: the host is statically allowed and identical either way, so
  // only the target's port can decide — which also proves the port crosses.
  writeFileSync(portMod, `export default (config) => ({
    onConnect(target) {
      if (target.port === config.block) return { action: "deny", reason: "port is closed" };
    },
  });`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    allow: ["127.0.0.1"],
    chain: [{ name: "ports", module: portMod, config: { block: uport } }],
  });

  const res = await forwardProxy(socket, `http://127.0.0.1:${uport}/x`, "GET", "");
  expect(res.status).toBe(403);
  expect(res.body).not.toContain("reached the upstream");
  const denial = ledgerLines(ledger).find((l) => l.port === uport);
  expect(denial?.verdict).toBe("deny-module");
  expect(denial?.proto).toBe("http");

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
  upstream.close();
});

test("a module outside a window with only content verbs still warns, and is never consulted", async () => {
  const upstream = http.createServer((_req, res) => { res.writeHead(200); res.end("reached the upstream"); });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const uport = (upstream.address() as net.AddressInfo).port;

  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-warn-"));
  const calls = join(work, "calls.jsonl");
  const contentMod = join(work, "content.ts");
  writeFileSync(contentMod, `import { appendFileSync } from "node:fs";
export default (config) => ({
  onRequest(head) { appendFileSync(config.calls, JSON.stringify({ path: head.path }) + "\\n"); },
});`);

  const socket = join(work, "engine.sock");
  const warnings: string[] = [];
  const realWarn = console.warn;
  console.warn = (...a: unknown[]) => { warnings.push(a.map(String).join(" ")); };
  let eng: { stop: () => void };
  try {
    eng = await start({
      socket,
      caDir: join(work, "ca"),
      allow: ["127.0.0.1"],
      chain: [{ name: "contentonly", module: contentMod, config: { calls } }],
    });
  } finally { console.warn = realWarn; }

  expect(warnings.join("\n")).toContain(`proxy hook "contentonly" is OUTSIDE a "tls: terminate" window`);

  // It is not a connection hop either: the request goes through untouched and
  // its content verb never ran.
  const res = await forwardProxy(socket, `http://127.0.0.1:${uport}/x`, "GET", "");
  expect(res.status).toBe(200);
  expect(res.body).toBe("reached the upstream");
  expect(existsSync(calls)).toBe(false);

  eng.stop();
  upstream.close();
});

test("a module outside a window that exports onConnect draws no content-verb warning", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-nowarn-"));
  const gateMod = join(work, "gate.ts");
  writeFileSync(gateMod, `export default () => ({ onConnect() {} });`);

  const warnings: string[] = [];
  const realWarn = console.warn;
  console.warn = (...a: unknown[]) => { warnings.push(a.map(String).join(" ")); };
  let eng: { stop: () => void };
  try {
    eng = await start({ socket: join(work, "engine.sock"), caDir: join(work, "ca"), chain: [{ name: "gate", module: gateMod, config: {} }] });
  } finally { console.warn = realWarn; }

  expect(warnings.join("\n")).not.toContain("OUTSIDE");
  eng.stop();
});

// --- an async onConnect must not cost the connection its bytes. --------------
// The gate runs inside the accept handler's `once("data")`. Between that
// listener firing and the reader the next stage attaches, the socket is flowing
// with nothing listening — and awaiting a hook widens that window to the hook's
// whole duration. These two drive bytes INTO the window (the box writes after
// the head, without waiting) and prove they still arrive.

// slowGateModule writes a module whose onConnect resolves after `delay` ms —
// the ordinary async shape the Handler contract advertises.
function slowGateModule(dir: string, delay: number): string {
  const p = join(dir, "slow.ts");
  writeFileSync(p, `export default (config) => ({
    async onConnect() { await new Promise((r) => setTimeout(r, config.delay)); },
  });`);
  return p;
}

test("an async onConnect keeps the plain forward-proxy body that arrives while it thinks", async () => {
  const upstream = http.createServer((req, res) => {
    let b = "";
    req.on("data", (d) => (b += d));
    req.on("end", () => { res.writeHead(200); res.end(`upstream saw [${b}]`); });
  });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const uport = (upstream.address() as net.AddressInfo).port;

  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-async-"));
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    allow: ["127.0.0.1"],
    chain: [{ name: "slow", module: slowGateModule(work, 40), config: { delay: 40 } }],
  });

  // The box writes the head, then the body 10ms later — inside the hook's 40ms.
  const body = "the body that arrives mid-verdict";
  const res = await new Promise<string>((resolve, reject) => {
    const c = net.connect(socket, () => {
      c.write(`POST http://127.0.0.1:${uport}/x HTTP/1.1\r\nHost: 127.0.0.1:${uport}\r\nConnection: close\r\nContent-Length: ${body.length}\r\n\r\n`);
      setTimeout(() => c.write(body), 10);
    });
    let data = "";
    c.on("data", (d) => (data += d.toString("latin1")));
    c.on("end", () => resolve(data));
    c.on("error", reject);
    setTimeout(() => reject(new Error("the request never completed — bytes were dropped while the hook awaited")), 4000);
  });

  expect(res).toContain("200");
  expect(res).toContain(`upstream saw [${body}]`);

  eng.stop();
  upstream.close();
}, 10_000);

test("an async onConnect keeps a CONNECT tunnel's first bytes that arrive while it thinks", async () => {
  const upstream = http.createServer((_req, res) => { res.writeHead(200); res.end("tunnelled to the upstream"); });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const uport = (upstream.address() as net.AddressInfo).port;

  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-async-connect-"));
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    allow: ["127.0.0.1"],
    chain: [{ name: "slow", module: slowGateModule(work, 40), config: { delay: 40 } }],
  });

  // A client that does not wait for the 200 before sending: its first tunnel
  // bytes land 10ms in, inside the hook's 40ms. With no terminate window the
  // engine raw-tunnels them to the upstream, so the reply proves they survived.
  const res = await new Promise<string>((resolve, reject) => {
    const c = net.connect(socket, () => {
      c.write(`CONNECT 127.0.0.1:${uport} HTTP/1.1\r\nHost: 127.0.0.1:${uport}\r\n\r\n`);
      setTimeout(() => c.write(`GET /x HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n`), 10);
    });
    let data = "";
    c.on("data", (d) => (data += d.toString("latin1")));
    c.on("end", () => resolve(data));
    c.on("error", reject);
    setTimeout(() => reject(new Error("the tunnel never carried the request — bytes were dropped while the hook awaited")), 4000);
  });

  expect(res).toContain("200 Connection Established");
  expect(res).toContain("tunnelled to the upstream");

  eng.stop();
  upstream.close();
}, 10_000);

test("a mixed hop outside a window is consulted for the connection AND warned about its content verbs", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-mixed-"));
  const mixedMod = join(work, "mixed.ts");
  writeFileSync(mixedMod, `export default () => ({
    onConnect(target) { if (target.host === "blocked.example.com") return { action: "deny", reason: "mixed hop said no" }; },
    onRequest() { return { action: "deny" }; },
  });`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const warnings: string[] = [];
  const realWarn = console.warn;
  console.warn = (...a: unknown[]) => { warnings.push(a.map(String).join(" ")); };
  let eng: { stop: () => void };
  try {
    eng = await start({ socket, caDir: join(work, "ca"), chain: [{ name: "mixed", module: mixedMod, config: {} }] });
  } finally { console.warn = realWarn; }

  // The content verbs cannot run out here, and saying so is the whole point of
  // the warning — a working onConnect must not buy silence about the rest.
  expect(warnings.join("\n")).toContain(`proxy hook "mixed" is OUTSIDE a "tls: terminate" window`);
  // Its connection verb is live all the same.
  expect(await connectLine(socket, "blocked.example.com", 443)).toContain("403");
  expect(ledgerLines(ledger).find((l) => l.host === "blocked.example.com")?.reason).toBe("mixed hop said no");

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
});

test("a hook cannot choose how many bytes its denial costs the host", async () => {
  const work = mkdtempSync(join(tmpdir(), "dabs-onconnect-reason-"));
  const shoutMod = join(work, "shout.ts");
  writeFileSync(shoutMod, `export default () => ({
    onConnect() { return { action: "deny", reason: "x".repeat(50000) }; },
  });`);

  const ledger = join(work, "connect.jsonl");
  process.env.DABS_CONNECT_LOG = ledger;
  const socket = join(work, "engine.sock");
  const eng = await start({ socket, caDir: join(work, "ca"), chain: [{ name: "shout", module: shoutMod, config: {} }] });

  // A module denial is recorded in the ledger and nowhere else — no per-attempt
  // host stderr, exactly as a static deny behaves.
  const noise: string[] = [];
  const realWarn = console.warn, realError = console.error;
  console.warn = (...a: unknown[]) => { noise.push(a.map(String).join(" ")); };
  console.error = (...a: unknown[]) => { noise.push(a.map(String).join(" ")); };
  try {
    expect(await connectLine(socket, "loud.example.com", 443)).toContain("403");
  } finally { console.warn = realWarn; console.error = realError; }
  expect(noise).toEqual([]);

  const reason = ledgerLines(ledger).find((l) => l.host === "loud.example.com")?.reason as string;
  expect(reason.length).toBeLessThanOrEqual(201); // the cap, plus the ellipsis marking the cut
  expect(reason.startsWith("xxx")).toBe(true);

  delete process.env.DABS_CONNECT_LOG;
  eng.stop();
});
