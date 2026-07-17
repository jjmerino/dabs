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

writeFileSync(
  recorderMod,
  `import { appendFileSync, mkdirSync } from "node:fs";
export default (config) => {
  mkdirSync(config.dir, { recursive: true });
  return {
    onRequest(req) {
      appendFileSync(config.dir + "/log.jsonl", JSON.stringify({ kind: "request", path: req.path, host: req.host }) + "\\n");
      return { action: "forward" };
    },
    onResponse(res) {
      appendFileSync(config.dir + "/log.jsonl", JSON.stringify({ kind: "response", status: res.status, body: res.body }) + "\\n");
    },
  };
};
`,
);

writeFileSync(
  responderMod,
  `export default () => ({
  onRequest(req) {
    if (req.path === "/fake/hello") {
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
        const body = data.slice(data.indexOf("\r\n\r\n") + 4);
        const status = Number(head.split("\r\n")[0].split(" ")[1]);
        resolve({ status, body });
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
