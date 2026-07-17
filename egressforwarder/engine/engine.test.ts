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
