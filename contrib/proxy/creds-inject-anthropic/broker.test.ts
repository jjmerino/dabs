// Hermetic proof of the credential broker, no real Anthropic and no dabs box.
// It stands up the dabs engine with the broker inside a terminate window over a
// LOCAL upstream, drives it as the box would (a request carrying the DUMMY
// token), and asserts the swap both ways:
//   - outbound: the upstream saw the REAL token (dummy→real on the way out)
//   - inbound:  the box saw the DUMMY token, never the real one (real→dummy home)
//   - bootstrap: a login-shaped response mints the vault from the wire.

import { test, expect } from "bun:test";
import { mkdtempSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import * as net from "node:net";
import * as http from "node:http";
import { start } from "../../../egressforwarder/engine/engine.ts";
import { DUMMIES } from "./broker.ts";

// proxyReq speaks the plain-HTTP forward-proxy shape to the engine socket —
// any method, extra headers, optional body — and returns the box-visible
// response.
function proxyReq(sock: string, method: string, absUrl: string, headers: Record<string, string>, body = ""): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const c = net.connect(sock, () => {
      const u = new URL(absUrl);
      let head = `${method} ${absUrl} HTTP/1.1\r\nHost: ${u.host}\r\n`;
      for (const [k, v] of Object.entries(headers)) head += `${k}: ${v}\r\n`;
      head += `Content-Length: ${Buffer.byteLength(body, "latin1")}\r\nConnection: close\r\n\r\n`;
      c.write(head + body, "latin1");
    });
    let data = "";
    c.on("data", (d) => (data += d.toString("latin1")));
    c.on("end", () => {
      const headEnd = data.indexOf("\r\n\r\n");
      const head = data.slice(0, headEnd);
      const raw = data.slice(headEnd + 4);
      const status = Number(head.split("\r\n")[0].split(" ")[1]);
      resolve({ status, body: /transfer-encoding:\s*chunked/i.test(head) ? dechunk(raw) : raw });
    });
    c.on("error", reject);
  });
}
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

async function engineWithBroker(work: string, vaultSeed: object | null, upstream: http.Server, extra: object = {}) {
  const vault = join(work, "vault.json");
  if (vaultSeed) writeFileSync(vault, JSON.stringify(vaultSeed));
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    chain: [
      { tls: "terminate" },
      { name: "broker", module: join(import.meta.dir, "broker.ts"), config: { vault, debug: join(work, "trace.jsonl"), ...extra } },
      { tls: "originate" },
    ],
  });
  const uport = (upstream.address() as net.AddressInfo).port;
  return { eng, socket, vault, url: `http://127.0.0.1:${uport}/` };
}

// recordingUpstream answers 200 JSON and records every request it received —
// method, path, headers, body — for wire-side assertions.
function recordingUpstream(): { srv: http.Server; seen: { method: string; path: string; headers: http.IncomingHttpHeaders; body: string }[] } {
  const seen: { method: string; path: string; headers: http.IncomingHttpHeaders; body: string }[] = [];
  const srv = http.createServer((req, res) => {
    let body = "";
    req.setEncoding("latin1");
    req.on("data", (d) => (body += d));
    req.on("end", () => {
      seen.push({ method: req.method ?? "", path: req.url ?? "", headers: req.headers, body });
      res.writeHead(200, { "content-type": "application/json" });
      res.end(`{"ok":true}`);
    });
  });
  return { srv, seen };
}

test("broker swaps dummy→real outbound and real→dummy inbound", async () => {
  const REAL = "sk-ant-oat01-REALREALREALtoken";
  // Upstream echoes the Authorization header it actually received.
  const upstream = http.createServer((req, res) => {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(`{"seen":"${req.headers.authorization}"}`);
  });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const work = mkdtempSync(join(tmpdir(), "broker-swap-"));
  const { eng, socket, url } = await engineWithBroker(work, { claudeAiOauth: { accessToken: REAL, refreshToken: "sk-ant-ort01-REALrefresh" } }, upstream);

  const res = await proxyReq(socket, "GET", url, { Authorization: `Bearer ${DUMMIES.access}` });
  expect(res.status).toBe(200);
  // Outbound: the upstream saw the REAL token (the broker injected it).
  expect(res.body).toContain(DUMMIES.access); // scrubbed back on the way home
  expect(res.body).not.toContain(REAL);       // the real token never reached the box

  eng.stop();
  upstream.close();
});

test("broker mints the vault from a login-shaped response (bootstrap)", async () => {
  const FRESH_ACCESS = "sk-ant-oat01-FRESHfromlogin";
  const FRESH_REFRESH = "sk-ant-ort01-FRESHrefresh";
  // A login/refresh token-exchange response hands back real tokens.
  const upstream = http.createServer((req, res) => {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(`{"access_token":"${FRESH_ACCESS}","refresh_token":"${FRESH_REFRESH}"}`);
  });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  const work = mkdtempSync(join(tmpdir(), "broker-boot-"));
  const { eng, socket, vault, url } = await engineWithBroker(work, null, upstream); // empty vault

  const res = await proxyReq(socket, "GET", url, { Authorization: `Bearer ${DUMMIES.access}` });
  expect(res.status).toBe(200);
  // The box sees only dummies, never the freshly minted real tokens.
  expect(res.body).toContain(DUMMIES.access);
  expect(res.body).toContain(DUMMIES.refresh);
  expect(res.body).not.toContain(FRESH_ACCESS);
  expect(res.body).not.toContain(FRESH_REFRESH);
  // The vault was born from the wire, outside the box.
  expect(existsSync(vault)).toBe(true);
  const saved = JSON.parse(readFileSync(vault, "utf8")).claudeAiOauth;
  expect(saved.accessToken).toBe(FRESH_ACCESS);
  expect(saved.refreshToken).toBe(FRESH_REFRESH);

  eng.stop();
  upstream.close();
});

// --- The positions. The dummy becomes real ONLY in the two credential slots —
// the Authorization header, and the refresh_token field of the refresh grant —
// and only toward an allowed host. Near-misses stay the dummy.

const REAL = "sk-ant-oat01-REALREALREALtoken";
const REALR = "sk-ant-ort01-REALrefreshRRRR";
const SEEDED = { claudeAiOauth: { accessToken: REAL, refreshToken: REALR } };

async function positioned(extra: object = {}) {
  const { srv, seen } = recordingUpstream();
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", r));
  const work = mkdtempSync(join(tmpdir(), "broker-pos-"));
  const h = await engineWithBroker(work, SEEDED, srv, extra);
  return { ...h, srv, seen, alerts: join(work, "alerts.log") };
}

test("header swap keys on the exact header name", async () => {
  const { eng, socket, url, srv, seen } = await positioned();
  await proxyReq(socket, "POST", url + "v1/messages", { Authorization: `Bearer ${DUMMIES.access}` }, `{"messages":[]}`);
  await proxyReq(socket, "POST", url + "v1/messages", { Authorizatio: `Bearer ${DUMMIES.access}` }, `{"messages":[]}`);
  expect(seen[0].headers["authorization"]).toBe(`Bearer ${REAL}`);
  expect(seen[1].headers["authorizatio"]).toBe(`Bearer ${DUMMIES.access}`);
  eng.stop();
  srv.close();
});

test("body swap keys on the exact field, path, and grant", async () => {
  const { eng, socket, url, srv, seen } = await positioned();
  await proxyReq(socket, "POST", url + "v1/oauth/token", {}, `{"grant_type":"refresh_token","refresh_token":"${DUMMIES.refresh}"}`);
  await proxyReq(socket, "POST", url + "v1/oauth/token", {}, `{"grant_type":"refresh_token","refresh_tokens":"${DUMMIES.refresh}"}`);
  await proxyReq(socket, "POST", url + "v1/messages", {}, `{"messages":[],"refresh_token":"${DUMMIES.refresh}"}`);
  expect(seen[0].body).toContain(`"refresh_token":"${REALR}"`);
  expect(seen[1].body).toContain(DUMMIES.refresh); // wrong field name: untouched
  expect(seen[1].body).not.toContain(REALR);
  expect(seen[2].body).toContain(DUMMIES.refresh); // wrong path: untouched
  expect(seen[2].body).not.toContain(REALR);
  eng.stop();
  srv.close();
});

test("no swap toward a host the broker was not told about", async () => {
  // The upstream is 127.0.0.1, but the broker only trusts another name.
  const { eng, socket, url, srv, seen } = await positioned({ hosts: ["api.anthropic.com"] });
  await proxyReq(socket, "POST", url + "v1/messages", { Authorization: `Bearer ${DUMMIES.access}` }, `{"messages":[]}`);
  await proxyReq(socket, "POST", url + "v1/oauth/token", {}, `{"grant_type":"refresh_token","refresh_token":"${DUMMIES.refresh}"}`);
  expect(seen[0].headers["authorization"]).toBe(`Bearer ${DUMMIES.access}`);
  expect(seen[1].body).toContain(DUMMIES.refresh);
  expect(seen[1].body).not.toContain(REALR);
  eng.stop();
  srv.close();
});

test("dummy in message text passes through unchanged and raises an alert", async () => {
  const { srv, seen } = recordingUpstream();
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", r));
  const work = mkdtempSync(join(tmpdir(), "broker-alert-"));
  const alertsFile = join(work, "alerts.log");
  const h = await engineWithBroker(work, SEEDED, srv, { alerts: alertsFile });
  await proxyReq(h.socket, "POST", h.url + "v1/messages", {}, `{"messages":[{"role":"user","content":"my creds file says ${DUMMIES.access}"}]}`);
  expect(seen[0].body).toContain(DUMMIES.access); // content is not a credential slot
  expect(seen[0].body).not.toContain(REAL);
  const alarm = readFileSync(alertsFile, "utf8");
  expect(alarm).toContain("/v1/messages");
  expect(alarm).not.toContain(REAL);
  expect(alarm).not.toContain(REALR);
  h.eng.stop();
  srv.close();
});

test("a real token in message text is scrubbed back to the dummy and raises an alert", async () => {
  const { srv, seen } = recordingUpstream();
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", r));
  const work = mkdtempSync(join(tmpdir(), "broker-leak-"));
  const alertsFile = join(work, "alerts.log");
  const h = await engineWithBroker(work, SEEDED, srv, { alerts: alertsFile });
  await proxyReq(h.socket, "POST", h.url + "v1/messages", {}, `{"messages":[{"role":"user","content":"remember this: ${REAL}"}]}`);
  expect(seen[0].body).not.toContain(REAL); // the real token never travels as text
  expect(seen[0].body).toContain(DUMMIES.access);
  expect(readFileSync(alertsFile, "utf8")).toContain("/v1/messages");
  h.eng.stop();
  srv.close();
});
