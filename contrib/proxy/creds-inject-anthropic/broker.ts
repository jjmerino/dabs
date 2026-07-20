// dabs Anthropic credential broker — a streaming egress content hook.
//
// This is Anthropic-specific by construction: it recognizes ONLY Anthropic OAuth
// tokens (the sk-ant-oat01-/sk-ant-ort01- formats) and the Claude Code login and
// refresh shapes. It is not a generic OAuth broker.
//
// The box runs normal Claude Code with a FULLY DUMMY, read-only credentials
// file; the REAL tokens live only in a host-side vault, born on the first in-box
// `/login` (the OAuth token-exchange response, intercepted here).
//
// Outbound, the dummy becomes real in exactly TWO POSITIONS — each defined by
// name, place, and host, never by what the bytes look like:
//
//   the `Authorization` header                     on requests to an allowed host
//   the `refresh_token` field of the JSON body     on POST /v1/oauth/token to an
//                                                  allowed host, grant_type
//                                                  refresh_token
//
// Everything else a request carries is CONTENT. A token string in content —
// the dummy (the agent has been reading its own credentials file) or a real
// one (a credential trying to travel as conversation) — is never expanded;
// a real token is rewritten back to its dummy before the request leaves, and
// the sighting is recorded host-side via `alerts`. The box sees none of this.
//
// Inbound (Anthropic → box) every real token is rewritten to its dummy, and
// rotation is captured to the vault — the box only ever holds the sentinels.
//
// This is a pure content hook over the dabs engine's four verbs. It runs INSIDE
// a `tls: terminate` window, never touches a socket, and never buffers a
// streaming (SSE) message response — those pass through live. The only request
// body it buffers is the refresh grant's own (small, and the one body that
// must be parsed, not scanned).
//
//   egress:
//     allow: api.anthropic.com
//     http_proxy:
//       - tls: terminate
//         domains: [api.anthropic.com]
//       - module: ./broker.ts
//         vault: ~/.dabs/broker/vault.json   # optional; this is the default
//         alerts: ~/.dabs/broker/alerts.log  # optional; token-in-content sightings
//         hosts: [api.anthropic.com]         # optional; hosts whose requests may
//                                            # carry credentials (default: any
//                                            # host inside the window)
//       - tls: originate

import { readFileSync, writeFileSync, appendFileSync, mkdirSync, chmodSync } from "node:fs";

// The dummy sentinels: format-valid OAuth tokens (Claude Code accepts them in
// its creds file) that are ALSO exactly what the box's read-only creds file
// contains. The broker recognizes these exact strings outbound. 108 chars each.
const pad = (p: string) => (p + "DABSBROKERDUMMY" + "0".repeat(108)).slice(0, 108);
const DUMMY_ACCESS = pad("sk-ant-oat01-");
const DUMMY_REFRESH = pad("sk-ant-ort01-");

// Any real Anthropic OAuth token (sk-ant-oat01-/sk-ant-ort01-) in a response is
// captured (bootstrapping the vault on the first login, tracking rotation on
// refresh) and rewritten to its dummy. Non-Anthropic tokens are not recognized.
const OAT = /sk-ant-oat01-[A-Za-z0-9_-]+/g;
const ORT = /sk-ant-ort01-[A-Za-z0-9_-]+/g;

interface Ctx {
  sse?: boolean;
  buf?: string; // response body accumulator (non-SSE)
  method?: string;
  host?: string;
  path?: string;
  credRequest?: boolean; // this request is the refresh grant — its body is parsed
  reqBuf?: string; // the refresh grant's body accumulator
  alerted?: boolean;
}
interface Head { method: string; path: string; host: string; headers: Record<string, string> }

function expandHome(p: string): string {
  return p.startsWith("~/") ? `${process.env.HOME}/${p.slice(2)}` : p;
}

export default (config: { vault?: string; alerts?: string; hosts?: string[] }) => {
  const vault = expandHome(config.vault ?? `${process.env.HOME}/.dabs/broker/vault.json`);
  const alerts = config.alerts ? expandHome(config.alerts) : "";
  // `hosts` restricts which hosts' requests may carry credentials. Absent, any
  // host inside the terminate window qualifies — the window is the scope.
  const hosts = config.hosts;
  const hostAllowed = (h: string) => !hosts || hosts.includes(h);

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

  function persist() {
    // The vault holds the REAL tokens, so keep it owner-only — the umask default
    // (0644) would leave a live credential world-readable. writeFileSync/mkdirSync
    // apply a mode only on CREATE, so chmod after to tighten a vault or dir that
    // already existed.
    const dir = vault.replace(/\/[^/]+$/, "");
    try { mkdirSync(dir, { recursive: true, mode: 0o700 }); } catch {}
    writeFileSync(vault, JSON.stringify({ claudeAiOauth: { accessToken: realAccess, refreshToken: realRefresh } }), { mode: 0o600 });
    try { chmodSync(dir, 0o700); } catch {}
    try { chmodSync(vault, 0o600); } catch {}
  }

  // alert records a token sighting in request CONTENT to the host-side alerts
  // file: one line per request, naming the request but never a token — an
  // alert must not be worth stealing.
  function alert(ctx: Ctx, reason: string) {
    if (!alerts || ctx.alerted) return;
    ctx.alerted = true;
    try {
      appendFileSync(alerts, `${new Date().toISOString()} ${ctx.method} ${ctx.host}${ctx.path} ${reason}\n`);
    } catch {}
  }

  // swapRefreshField expands the dummy in the ONE body position that is a
  // credential slot: the `refresh_token` field of the refresh grant. The body
  // is parsed, the exact field replaced, anything else left as it came.
  function swapRefreshField(s: string): string {
    try {
      const o = JSON.parse(s);
      if (o?.grant_type === "refresh_token" && o.refresh_token === DUMMY_REFRESH) {
        o.refresh_token = realRefresh;
        return JSON.stringify(o);
      }
    } catch {
      // Not JSON — not the refresh grant's shape; pass through untouched.
    }
    return s;
  }

  // scrub rewrites any real token in a RESPONSE back to its dummy, capturing
  // the real value to the vault the first time it is seen (login bootstrap,
  // refresh rotation).
  function scrub(s: string): string {
    let captured = false;
    const out = s
      .replace(OAT, (m) => (m === DUMMY_ACCESS ? m : ((realAccess = m), (captured = true), DUMMY_ACCESS)))
      .replace(ORT, (m) => (m === DUMMY_REFRESH ? m : ((realRefresh = m), (captured = true), DUMMY_REFRESH)));
    if (captured) persist();
    return out;
  }

  return {
    onRequest(head: Head, ctx: Ctx) {
      ctx.method = head.method;
      ctx.host = head.host;
      ctx.path = head.path;
      const path = head.path.split("?")[0];
      ctx.credRequest = hostAllowed(head.host) && head.method === "POST" && path === "/v1/oauth/token";
      // Position one: the Authorization header, on an allowed host only.
      const auth = head.headers["authorization"];
      if (auth && hostAllowed(head.host)) {
        head.headers["authorization"] = auth.split(DUMMY_ACCESS).join(realAccess);
      }
      // Opt out of compression so the response body is plaintext for the
      // scrub — the box's client re-negotiates its own encoding upstream.
      delete head.headers["accept-encoding"];
      return { headers: head.headers };
    },
    onRequestChunk(chunk: Buffer | null, ctx: Ctx) {
      // Position two: the refresh grant's body — buffered whole (it is small)
      // and parsed at EOF, so the swap lands on the exact field, not on a
      // pattern.
      if (ctx.credRequest) {
        if (chunk === null) {
          const s = ctx.reqBuf ?? "";
          ctx.reqBuf = "";
          return Buffer.from(swapRefreshField(s), "latin1");
        }
        ctx.reqBuf = (ctx.reqBuf ?? "") + chunk.toString("latin1");
        return Buffer.alloc(0); // hold until EOF, then emit the swapped body
      }
      // Every other body is CONTENT. The dummy passes through unchanged (and
      // is worth an alarm: the agent read its credentials file). A real token
      // is rewritten back to its dummy before it leaves — a credential never
      // travels as conversation.
      if (chunk === null) return;
      let s = chunk.toString("latin1");
      const sawDummy = s.includes(DUMMY_ACCESS) || s.includes(DUMMY_REFRESH);
      const sawReal = (realAccess !== "" && s.includes(realAccess)) || (realRefresh !== "" && s.includes(realRefresh));
      if (!sawDummy && !sawReal) return; // pass non-token chunks through unchanged
      alert(ctx, "token-in-content");
      if (!sawReal) return;
      if (realAccess !== "") s = s.split(realAccess).join(DUMMY_ACCESS);
      if (realRefresh !== "") s = s.split(realRefresh).join(DUMMY_REFRESH);
      return Buffer.from(s, "latin1");
    },
    onResponse(head: { status?: number; headers: Record<string, string> }, ctx: Ctx) {
      // A streaming message response (SSE) carries NO tokens — pass it through
      // live, never accumulate. Everything else (the login/refresh JSON) is
      // small; the broker accumulates it ITSELF and scrubs at EOF, so the engine
      // never buffers on its behalf.
      const ct = head.headers["content-type"] ?? "";
      ctx.sse = ct.includes("text/event-stream");
    },
    onResponseChunk(chunk: Buffer | null, ctx: Ctx) {
      if (ctx.sse) return; // live passthrough, no tokens
      if (chunk === null) {
        const body = ctx.buf ?? "";
        ctx.buf = "";
        const scrubbed = scrub(body);
        return scrubbed ? Buffer.from(scrubbed, "latin1") : null;
      }
      ctx.buf = (ctx.buf ?? "") + chunk.toString("latin1");
      return Buffer.alloc(0); // hold until EOF, then emit the scrubbed body
    },
  };
};

// Exported for tests: the dummy sentinels a confined box's creds file carries.
export const DUMMIES = { access: DUMMY_ACCESS, refresh: DUMMY_REFRESH };
