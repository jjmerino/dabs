// dabs credential broker — a streaming egress content hook.
//
// The box runs normal Claude Code with a FULLY DUMMY, read-only credentials
// file; the REAL tokens live only in a host-side vault, born on the first in-box
// `/login` (the OAuth token-exchange response, intercepted here). The broker is
// a bidirectional string swap on the api.anthropic.com traffic it terminates:
//
//   outbound (box → Anthropic): DUMMY token → real token   (Authorization header,
//                                                            and the refresh body)
//   inbound  (Anthropic → box): real token → DUMMY token   (incl. rotation)
//
// So the box only ever holds the dummy sentinels; the real tokens never reach it.
// The inbound real→dummy pass is the by-construction guarantee that Claude is
// never handed a real credential — the read-only dummy mount is the backstop.
//
// This is a pure content hook over the dabs engine's four verbs. It runs INSIDE
// a `tls: terminate` window scoped to api.anthropic.com, so it only ever sees
// that host's decrypted HTTP. It never touches a socket and never buffers a
// streaming (SSE) message response — those carry no tokens and pass through live.
//
//   egress:
//     allow: api.anthropic.com
//     http_proxy:
//       - tls: terminate
//         domains: [api.anthropic.com]
//       - module: ./broker.ts
//         vault: ~/.dabs/broker/vault.json   # optional; this is the default
//         debug: /tmp/broker-trace.jsonl     # optional; a trace while shaping it
//       - tls: originate

import { readFileSync, writeFileSync, mkdirSync, appendFileSync } from "node:fs";

// The dummy sentinels: format-valid OAuth tokens (Claude Code accepts them in
// its creds file) that are ALSO exactly what the box's read-only creds file
// contains. The broker recognizes these exact strings outbound. 108 chars each.
const pad = (p: string) => (p + "DABSBROKERDUMMY" + "0".repeat(108)).slice(0, 108);
const DUMMY_ACCESS = pad("sk-ant-oat01-");
const DUMMY_REFRESH = pad("sk-ant-ort01-");

// Any real OAuth token in a response is captured (bootstrapping the vault on the
// first login, tracking rotation on refresh) and rewritten to its dummy.
const OAT = /sk-ant-oat01-[A-Za-z0-9_-]+/g;
const ORT = /sk-ant-ort01-[A-Za-z0-9_-]+/g;

interface Ctx { sse?: boolean; buf?: string }
interface Head { method: string; path: string; headers: Record<string, string> }

function expandHome(p: string): string {
  return p.startsWith("~/") ? `${process.env.HOME}/${p.slice(2)}` : p;
}

export default (config: { vault?: string; debug?: string }) => {
  const vault = expandHome(config.vault ?? `${process.env.HOME}/.dabs/broker/vault.json`);
  const debug = config.debug ? expandHome(config.debug) : "";

  // The real values live only here (and in the vault file). The vault may start
  // EMPTY: a new user's box has no credential, logs in, and the login's token
  // exchange (intercepted below) populates it — the credential is BORN outside
  // the box and stays there. Never logged.
  let realAccess = "";
  let realRefresh = "";
  try {
    const o = JSON.parse(readFileSync(vault, "utf8"))?.claudeAiOauth ?? {};
    realAccess = o.accessToken ?? "";
    realRefresh = o.refreshToken ?? "";
  } catch {
    // No vault yet — the first in-box login writes it.
  }

  const trace = (o: unknown) => { if (debug) { try { appendFileSync(debug, JSON.stringify(o) + "\n"); } catch {} } };

  const dummyToReal = (s: string) =>
    s.split(DUMMY_ACCESS).join(realAccess).split(DUMMY_REFRESH).join(realRefresh);

  function persist() {
    try { mkdirSync(vault.replace(/\/[^/]+$/, ""), { recursive: true }); } catch {}
    writeFileSync(vault, JSON.stringify({ claudeAiOauth: { accessToken: realAccess, refreshToken: realRefresh } }));
  }

  // scrub rewrites any real token back to its dummy, capturing the real value to
  // the vault the first time it is seen (login bootstrap, refresh rotation).
  function scrub(s: string): string {
    let captured = false;
    const out = s
      .replace(OAT, (m) => (m === DUMMY_ACCESS ? m : ((realAccess = m), (captured = true), DUMMY_ACCESS)))
      .replace(ORT, (m) => (m === DUMMY_REFRESH ? m : ((realRefresh = m), (captured = true), DUMMY_REFRESH)));
    if (captured) persist();
    trace({ t: "scrub", captured });
    return out;
  }

  return {
    onRequest(head: Head) {
      // Swap dummy→real on the Authorization header (messages AND the OAuth
      // refresh). Opt out of compression so the response body is plaintext for
      // the scrub — the box's client re-negotiates its own encoding upstream.
      const auth = head.headers["authorization"];
      if (auth) head.headers["authorization"] = dummyToReal(auth);
      delete head.headers["accept-encoding"];
      trace({ t: "req", method: head.method, path: head.path, hasAuth: !!auth });
      return { headers: head.headers };
    },
    onRequestChunk(chunk: Buffer | null) {
      // The refresh request carries the dummy refresh token in its JSON body.
      // Swap only when a dummy is actually present, else pass the chunk through.
      if (chunk === null) return;
      const s = chunk.toString("latin1");
      if (!s.includes(DUMMY_REFRESH) && !s.includes(DUMMY_ACCESS)) return;
      return Buffer.from(dummyToReal(s), "latin1");
    },
    onResponse(head: { headers: Record<string, string> }, ctx: Ctx) {
      // A streaming message response (SSE) carries NO tokens — pass it through
      // live, never accumulate. Everything else (the login/refresh JSON) is
      // small; the broker accumulates it ITSELF and scrubs at EOF, so the engine
      // never buffers on its behalf.
      ctx.sse = (head.headers["content-type"] ?? "").includes("text/event-stream");
    },
    onResponseChunk(chunk: Buffer | null, ctx: Ctx) {
      if (ctx.sse) return; // live passthrough, no tokens
      if (chunk === null) {
        const body = ctx.buf ?? "";
        ctx.buf = "";
        return body ? Buffer.from(scrub(body), "latin1") : null;
      }
      ctx.buf = (ctx.buf ?? "") + chunk.toString("latin1");
      return Buffer.alloc(0); // hold until EOF, then emit the scrubbed body
    },
  };
};

// Exported for tests: the dummy sentinels a confined box's creds file carries.
export const DUMMIES = { access: DUMMY_ACCESS, refresh: DUMMY_REFRESH };
