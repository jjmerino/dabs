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

// forwardProxy speaks the plain-HTTP forward-proxy shape to the engine socket,
// carrying the box's Authorization header, and returns the box-visible response.
function forwardProxy(sock: string, absUrl: string, auth: string): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const c = net.connect(sock, () => {
      const u = new URL(absUrl);
      c.write(`GET ${absUrl} HTTP/1.1\r\nHost: ${u.host}\r\nAuthorization: ${auth}\r\nConnection: close\r\n\r\n`);
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

async function engineWithBroker(work: string, vaultSeed: object | null, upstream: http.Server) {
  const vault = join(work, "vault.json");
  if (vaultSeed) writeFileSync(vault, JSON.stringify(vaultSeed));
  const socket = join(work, "engine.sock");
  const eng = await start({
    socket,
    caDir: join(work, "ca"),
    chain: [
      { tls: "terminate" },
      { name: "broker", module: join(import.meta.dir, "broker.ts"), config: { vault, debug: join(work, "trace.jsonl") } },
      { tls: "originate" },
    ],
  });
  const uport = (upstream.address() as net.AddressInfo).port;
  return { eng, socket, vault, url: `http://127.0.0.1:${uport}/` };
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

  const res = await forwardProxy(socket, url, `Bearer ${DUMMIES.access}`);
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

  const res = await forwardProxy(socket, url, `Bearer ${DUMMIES.access}`);
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
