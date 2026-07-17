// The dabs proxy engine: one in-process Bun program per box that runs the
// recipe's ordered egress proxy chain. The box's egress is pointed at this
// engine's unix socket (HTTP-proxy protocol); per connection the engine keys on
// the verified CONNECT host and runs each hook at up to two moments:
//
//   - CONNECTION check (EVERY module hop): the hook's `authorize({host, port})`
//     decides allow / deny at CONNECT, BEFORE any tunnel — no decryption.
//     This is the domain allow/deny tier, and it runs whether the connection is
//     then terminated OR passed through, so the domain policy always applies.
//   - CONTENT inspection (module hops INSIDE a `tls: terminate` … `tls: originate`
//     window): the engine terminates TLS with a CA the box trusts and runs the
//     hook's `onRequest`/`onResponse` on the decrypted request; `tls: originate`
//     re-encrypts to the real upstream. A hook outside a window has no plaintext,
//     so only its `authorize` runs.
//
// A chain with no `tls: terminate` needs no CA: allowed connections raw-tunnel to
// the real host. A `tls: terminate` is always closed by a `tls: originate` (the
// recipe validator requires it), so decrypted traffic that is forwarded is always
// re-encrypted — never downgraded to plaintext. A hook that `respond`s answers
// locally and breaks the chain before the close. A client that PINS its cert
// (rejects our leaf) is learned and passed through un-intercepted; its content
// hooks are skipped (no plaintext), but authorize still gated the connection.
// ECH (encrypted SNI) is refused: it would hide the destination from authorize.
//
// Contract (all fields plain JSON; a handler never touches a socket):
//   authorize({host, port})
//     → "allow" (or nothing)                          let the connection through
//     → "deny"  (or {action:"deny"})                  refuse the CONNECT (403)
//   onRequest({host, method, path, headers, body})
//     → {action:"forward", headers?, body?}           continue down the chain
//     → {action:"respond", status, headers?, body}    ANSWER here, break the chain
//     → {action:"deny", status?, headers?, body?}     refuse
//   onResponse({host, status, headers, body})
//     → {status?, headers?, body?}                    rewrite (or return nothing)
//
// A `respond` turns the request around: the response unwinds through onResponse
// of every hook the request already passed, in REVERSE order.

import * as net from "node:net";
import { existsSync, mkdirSync, unlinkSync, copyFileSync } from "node:fs";

type Json = Record<string, unknown>;

interface RequestView { host: string; method: string; path: string; headers: Record<string, string>; body: string; }
interface ResponseView { host: string; status: number; headers: Record<string, string>; body: string; }
interface Handler {
  authorize?(conn: { host: string; port: number }): Promise<Json | string | void> | Json | string | void;
  onRequest?(req: RequestView): Promise<Json | void> | Json | void;
  onResponse?(res: ResponseView): Promise<Json | void> | Json | void;
}
interface HopConfig { name?: string; tls?: string; domains?: string[]; module?: string; config?: Json; }
interface EngineConfig { socket: string; caDir: string; chain: HopConfig[]; }

interface Hop { name: string; kind: "tls-terminate" | "tls-originate" | "module"; handler?: Handler; domains?: string[]; }

// --- certs: a CA (minted once) and a per-host leaf signed by it, cached.

// runOpenssl runs openssl and FAILS LOUD on a non-zero exit — a swallowed failure
// caches an empty cert and breaks TLS termination for that host with no trace.
function runOpenssl(args: string[]) {
  const r = Bun.spawnSync(["openssl", ...args]);
  if (r.exitCode !== 0) {
    const msg = `openssl ${args[0]} failed (exit ${r.exitCode}): ${Buffer.from(r.stderr ?? []).toString().trim()}`;
    console.error(`warning: ${msg}`);
    throw new Error(msg);
  }
  return r;
}

export function ensureCA(dir: string) {
  mkdirSync(dir, { recursive: true });
  if (!existsSync(`${dir}/ca.crt`)) {
    runOpenssl(["genrsa", "-out", `${dir}/ca.key`, "2048"]);
    runOpenssl(["req", "-x509", "-new", "-nodes", "-key", `${dir}/ca.key`, "-sha256", "-days", "3650", "-out", `${dir}/ca.crt`, "-subj", "/CN=dabs proxy CA"]);
  }
  // Publish ONLY the public cert into a directory of its own. A driver mounts
  // that directory into the box (the apple micro-VM cannot bind a single file),
  // so the box trusts the CA without the private key (ca.key stays out of pub/).
  mkdirSync(`${dir}/pub`, { recursive: true });
  copyFileSync(`${dir}/ca.crt`, `${dir}/pub/ca.crt`);
}

const leafCache = new Map<string, Promise<{ cert: string; key: string }>>();
// Single-flight: memoize the in-flight Promise synchronously so two concurrent
// first-contacts to the same host share ONE mint. Otherwise both would run
// openssl against the same base path and interleave, and the first connection
// could be served a half-written cert (no SAN → the client rejects it).
export function leafFor(dir: string, host: string): Promise<{ cert: string; key: string }> {
  const cached = leafCache.get(host);
  if (cached) return cached;
  const p = mintLeaf(dir, host);
  p.catch(() => leafCache.delete(host)); // a failed mint must not poison the cache
  leafCache.set(host, p);
  return p;
}
async function mintLeaf(dir: string, host: string): Promise<{ cert: string; key: string }> {
  const base = `${dir}/leaf-${host.replace(/[^a-zA-Z0-9.-]/g, "_").slice(0, 40)}-${Bun.hash(host).toString(36)}`;
  runOpenssl(["genrsa", "-out", `${base}.key`, "2048"]);
  // A constant CN: X.509 CommonName caps at 64 chars, so a long host would make
  // openssl reject the CSR. Hostname matching lives in the SAN below (which has no
  // such limit), and modern TLS ignores CN when a SAN is present.
  runOpenssl(["req", "-new", "-key", `${base}.key`, "-out", `${base}.csr`, "-subj", `/CN=dabs-leaf`]);
  // A literal IP host needs an iPAddress SAN — a DNS SAN of "1.1.1.1" does not
  // match, and the client rejects the leaf. Otherwise it is a DNS name.
  const isIP = /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":");
  await Bun.write(`${base}.ext`, `subjectAltName=${isIP ? "IP" : "DNS"}:${host}\nextendedKeyUsage=serverAuth\n`);
  runOpenssl(["x509", "-req", "-in", `${base}.csr`, "-CA", `${dir}/ca.crt`, "-CAkey", `${dir}/ca.key`, "-CAcreateserial", "-out", `${base}.crt`, "-days", "3650", "-sha256", "-extfile", `${base}.ext`]);
  return { cert: await Bun.file(`${base}.crt`).text(), key: await Bun.file(`${base}.key`).text() };
}

// --- chain: load module hooks at boot; classify tls boundary directives.
async function resolveChain(chain: HopConfig[]): Promise<Hop[]> {
  const hops: Hop[] = [];
  for (const h of chain) {
    if (h.tls === "terminate" || h.tls === "originate") {
      hops.push({ name: "tls", kind: h.tls === "terminate" ? "tls-terminate" : "tls-originate", domains: h.domains });
      continue;
    }
    if (!h.module) throw new Error(`proxy hop: a chain entry must be a tls directive or a module`);
    const mod = await import(h.module);
    const factory = mod.default;
    if (typeof factory !== "function") throw new Error(`proxy module ${h.module}: default export must be a factory (config) => handler`);
    const handler = factory(h.config ?? {});
    // A factory that returns a non-object (null, a number, undefined) would become
    // a silent pass-through — no authorize, no hooks — while a bad default export
    // fails loudly. Fail closed here too: reject at boot rather than open egress.
    if (!handler || typeof handler !== "object") throw new Error(`proxy module ${h.module}: factory must return a handler object, got ${handler === null ? "null" : typeof handler}`);
    hops.push({ name: h.name ?? h.module, kind: "module", handler: handler as Handler });
  }
  return hops;
}

// Classify the chain. EVERY module hop's `authorize` runs at CONNECT (the domain
// check, whether we terminate or pass through), so moduleHops is all of them in
// order. contentHops is the subset inside a terminate…originate window — only
// those see decrypted content via onRequest/onResponse. Also report whether the
// chain terminates (needs a CA) and originates (forwards upstream).
function classify(hops: Hop[]) {
  const moduleHops: Hop[] = [];
  const contentHops: Hop[] = [];
  let inWindow = false, terminates = false, originates = false;
  let terminateDomains: string[] | null = null;
  for (const h of hops) {
    if (h.kind === "tls-terminate") {
      inWindow = true;
      terminates = true;
      // An empty/absent list means "terminate every host"; a list scopes it.
      terminateDomains = h.domains && h.domains.length ? h.domains.map((d) => d.toLowerCase()) : null;
      continue;
    }
    if (h.kind === "tls-originate") { inWindow = false; originates = true; continue; }
    moduleHops.push(h);
    if (inWindow) contentHops.push(h);
  }
  // Diagnostic: a hook with content methods but no window will never inspect
  // content (its authorize still runs). Say so at boot, loudly.
  for (const h of moduleHops) {
    if (!contentHops.includes(h) && (h.handler?.onRequest || h.handler?.onResponse)) {
      console.warn(`warning: proxy hook "${h.name}" is OUTSIDE a "tls: terminate" window — its onRequest/onResponse will NOT run (only its authorize does). Add "tls: terminate" before it to inspect content.`);
    }
  }
  return { moduleHops, contentHops, terminates, originates, terminateDomains };
}

// Hooks are user code, so bound every call: a hung or slow hook must not stall
// the box's egress forever. On timeout the promise rejects and the caller turns
// it into a clean refusal (deny / 502), never a hang. The timer is cleared when
// the hook settles, so a fast hook leaves nothing dangling.
const HOOK_TIMEOUT_MS = 100;
function callHook<T>(p: Promise<T> | T): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`proxy hook exceeded ${HOOK_TIMEOUT_MS}ms`)), HOOK_TIMEOUT_MS);
    Promise.resolve(p).then(
      (v) => { clearTimeout(timer); resolve(v); },
      (e) => { clearTimeout(timer); reject(e); },
    );
  });
}

// FAIL_OPEN is the policy when an authorize hook FAILS — throws, times out, or
// returns a verdict the engine does not recognize. false (the default) blocks
// egress on any such failure: a broken security control must not silently let
// traffic through. It is a single constant so a future recipe-schema knob can
// wire it per box without touching this logic.
const FAIL_OPEN = false;

// PASSTHROUGH_ON_TLS_FAILURE: a client that PINS its cert rejects our MITM leaf.
// A "tls: terminate" window is a declaration that the traffic MUST be inspected,
// so a host we cannot intercept must be REFUSED, not waved through: passthrough
// on a broker would let a credential the host was meant to swap out land inside
// the box. false (the default) fails closed — a pinned host on a terminate chain
// stays unreachable. true would learn the host and tunnel later connections
// through un-intercepted (graceful for observability-only chains, unsafe for any
// control). A single constant so a future recipe-schema knob — ideally per
// domain — can wire it per box without touching this logic.
const PASSTHROUGH_ON_TLS_FAILURE = false;

// ALLOW_ECH: Encrypted Client Hello hides the real SNI, so we cannot verify the
// destination a connection actually targets — a hole in the domain policy. false
// (the default) refuses any ClientHello carrying the ECH extension.
const ALLOW_ECH = false;

// helloHasECH parses a buffered TLS ClientHello for the encrypted_client_hello
// extension (0xfe0d). Best-effort: a malformed or non-handshake buffer is "no".
export function helloHasECH(buf: Buffer): boolean {
  try {
    if (buf.length < 43 || buf[0] !== 0x16 || buf[5] !== 0x01) return false; // not a ClientHello record
    let p = 5 + 4; // TLS record header (5) + handshake header (4)
    p += 2 + 32; // client_version + random
    p += 1 + buf[p]; // session_id
    p += 2 + buf.readUInt16BE(p); // cipher_suites
    p += 1 + buf[p]; // compression_methods
    if (p + 2 > buf.length) return false;
    const extEnd = Math.min(p + 2 + buf.readUInt16BE(p), buf.length);
    p += 2;
    while (p + 4 <= extEnd) {
      const type = buf.readUInt16BE(p);
      if (type === 0xfe0d) return true;
      p += 4 + buf.readUInt16BE(p + 2);
    }
    return false;
  } catch {
    return false;
  }
}

// firstChunk resolves with the next data event from a socket (the client's
// ClientHello after our CONNECT 200), or null if it closes first.
function firstChunk(sock: net.Socket): Promise<Buffer | null> {
  return new Promise((resolve) => {
    const onData = (d: Buffer) => { sock.removeListener("close", onClose); resolve(d); };
    const onClose = () => { sock.removeListener("data", onData); resolve(null); };
    sock.once("data", onData);
    sock.once("close", onClose);
  });
}

// --- CONNECTION tier: run authorize on each connection hook; allow or deny.
// A recognized "allow"/nothing lets it through; "deny" blocks. A throw, a
// timeout, or any unrecognized verdict is a FAILURE, handled per FAIL_OPEN
// (default: block).
// canonicalHost normalizes a host for policy: DNS names are case-insensitive and
// a trailing dot is the same server, so lowercase and strip it — otherwise
// "WWW.X.COM" or "www.x.com." would slip past a deny of "www.x.com".
function canonicalHost(host: string): string {
  return host.toLowerCase().replace(/\.+$/, "");
}

async function authorizeConn(hops: Hop[], host: string, port: number): Promise<{ allow: boolean; host: string; port: number }> {
  const failed = () => ({ allow: FAIL_OPEN, host, port });
  for (const hop of hops) {
    if (!hop.handler?.authorize) continue;
    let v: Json | string | void;
    try {
      v = await callHook(hop.handler.authorize({ host, port }));
    } catch {
      if (FAIL_OPEN) continue; // treat the failure as allow
      return failed();
    }
    const action = v && typeof v === "object" ? (v as Json).action : undefined;
    if (v == null || v === "allow" || action === "allow") continue; // explicit allow
    if (v === "deny" || action === "deny") return { allow: false, host, port };
    // Anything else is a malformed verdict — a failure.
    if (!FAIL_OPEN) return failed();
  }
  return { allow: true, host, port };
}

// --- CONTENT tier: onRequest in order until a hook responds (or forwards past
//     the last hook to the real upstream), then onResponse in reverse through
//     the hooks the request passed — INCLUDING the hook that responded, so a
//     single hook can both answer and post-process its own answer (the broker
//     swap-back pattern). scheme is https for a terminated TLS request, http for
//     a plain forward-proxy request.
async function runContent(hops: Hop[], forwardToUpstream: boolean, req: RequestView, scheme: "http" | "https" = "https"): Promise<ResponseView> {
  let response: ResponseView | null = null;
  let traversed = 0;

  for (let i = 0; i < hops.length; i++) {
    const verdict = (await callHook(hops[i].handler?.onRequest?.(req))) as Json | undefined;
    if (verdict?.action === "respond") {
      response = { host: req.host, status: Number(verdict.status ?? 200), headers: (verdict.headers as Record<string, string>) ?? {}, body: String(verdict.body ?? "") };
      traversed = i + 1; // include the responder in the onResponse unwind
      break;
    }
    if (verdict?.action === "deny") {
      // deny honors headers, just like respond — a refusal can carry policy metadata.
      response = { host: req.host, status: Number(verdict.status ?? 403), headers: (verdict.headers as Record<string, string>) ?? {}, body: String(verdict.body ?? "denied") };
      traversed = i + 1;
      break;
    }
    if (verdict?.headers) req.headers = { ...req.headers, ...(verdict.headers as Record<string, string>) };
    if (typeof verdict?.body === "string") req.body = verdict.body;
    traversed = i + 1;
  }

  if (!response) {
    response = forwardToUpstream
      ? await fetchUpstream(req, scheme)
      : { host: req.host, status: 502, headers: {}, body: "request forwarded past the last hook but the chain is terminal — add a hook that responds, or a `tls: originate` to reach the upstream" };
  }

  for (let i = traversed - 1; i >= 0; i--) {
    const rewrite = (await callHook(hops[i].handler?.onResponse?.(response))) as Json | undefined;
    if (rewrite) {
      if (rewrite.status != null) response.status = Number(rewrite.status);
      if (rewrite.headers) response.headers = { ...response.headers, ...(rewrite.headers as Record<string, string>) };
      if (typeof rewrite.body === "string") response.body = rewrite.body;
    }
  }
  return response;
}

async function fetchUpstream(req: RequestView, scheme: "http" | "https"): Promise<ResponseView> {
  const headers = new Headers();
  for (const [k, v] of Object.entries(req.headers)) if (k.toLowerCase() !== "host") headers.set(k, v);
  headers.set("host", req.host);
  // Bodies flow as latin1 byte-strings (1 char = 1 byte, lossless) so a hook can
  // inspect/rewrite text without corrupting a binary payload (images, gzip, wasm)
  // that merely passes through. .text() would UTF-8-decode and mangle those bytes.
  const reqBody = req.method === "GET" || req.method === "HEAD" ? undefined : Buffer.from(req.body, "latin1");
  const up = await fetch(`${scheme}://${req.host}${req.path}`, { method: req.method, headers, body: reqBody });
  return { host: req.host, status: up.status, headers: Object.fromEntries(up.headers), body: Buffer.from(await up.arrayBuffer()).toString("latin1") };
}

// --- boot ------------------------------------------------------------------
export async function start(cfg: EngineConfig): Promise<{ stop: () => void }> {
  const hops = await resolveChain(cfg.chain);
  const { moduleHops, contentHops, terminates, originates, terminateDomains } = classify(hops);

  // shouldInspect decides whether THIS host's TLS is terminated and its content
  // inspected. With no `domains` list a terminate window covers every host; a
  // list scopes it (exact host or a subdomain), so traffic we don't care about
  // passes through un-decrypted. authorize still runs either way.
  function shouldInspect(host: string): boolean {
    if (!terminates) return false;
    if (!terminateDomains) return true;
    return terminateDomains.some((d) => host === d || host.endsWith("." + d));
  }
  // Always mint the CA: dabs mounts ca.crt into every proxy box (the mount origin
  // must exist), and a chain that gains a `tls: terminate` later needs it anyway.
  ensureCA(cfg.caDir);

  // Hosts whose clients pin (rejected our leaf) — passed through un-intercepted.
  const pinnedHosts = new Set<string>();

  const terminators = new Map<string, Promise<string>>();
  function terminatorFor(host: string): Promise<string> {
    const existing = terminators.get(host);
    if (existing) return existing;
    // Single-flight like leafFor: memoize the in-flight Promise so two concurrent
    // first-contacts don't both Bun.serve the same socket.
    const p = makeTerminator(host);
    p.catch(() => terminators.delete(host));
    terminators.set(host, p);
    return p;
  }
  async function makeTerminator(host: string): Promise<string> {
    const leaf = await leafFor(cfg.caDir, host);
    // A unix socket path is capped (~108 bytes); a long hostname would overflow it
    // and fail opaquely. Keep the basename bounded — a readable prefix plus a hash
    // for uniqueness when the host is long.
    const safe = host.replace(/[^a-zA-Z0-9.-]/g, "_");
    const base = safe.length <= 40 ? safe : `${safe.slice(0, 24)}-${Bun.hash(host).toString(36)}`;
    const sock = `${cfg.caDir}/term-${base}.sock`;
    if (existsSync(sock)) { try { unlinkSync(sock); } catch {} }
    Bun.serve({
      unix: sock,
      tls: { cert: leaf.cert, key: leaf.key },
      async fetch(request) {
        const url = new URL(request.url);
        const req: RequestView = {
          host,
          method: request.method,
          path: url.pathname + url.search,
          headers: Object.fromEntries(request.headers),
          body: request.method === "GET" || request.method === "HEAD" ? "" : Buffer.from(await request.arrayBuffer()).toString("latin1"),
        };
        const res = await runContent(contentHops, originates, req);
        // Never let a hook throw or a bad status become Bun's default error page —
        // that page carries the engine's source and host paths into the untrusted
        // box. Catch everything, clamp the status, and answer with a terse 502.
        let status = Number(res.status);
        if (!Number.isInteger(status) || status < 200 || status > 599) status = 502;
        const h = new Headers(res.headers);
        h.delete("content-length");
        h.delete("content-encoding");
        // Serve the raw bytes: res.body is a latin1 byte-string, so re-encode it
        // 1:1 rather than let Response() UTF-8-encode and corrupt a binary body.
        return new Response(Buffer.from(res.body, "latin1"), { status, headers: h });
      },
      error(err) {
        // A hook that threw or timed out reaches here. Leave a diagnostic in the
        // engine log — the box gets a terse 502, but the operator can see WHY.
        console.error(`warning: proxy hook failed for ${host}: ${err?.message ?? err}`);
        return new Response("proxy engine error", { status: 502 });
      },
    });
    return sock;
  }

  // A plain-HTTP forward-proxy request (`GET http://host/path HTTP/1.1`) has no
  // TLS to terminate — the content is already plaintext, so `tls: terminate` is a
  // no-op and the content hops run directly on the request. authorize still gates
  // the connection; a chain with no content hops just forwards to the upstream.
  async function handlePlainHTTP(client: net.Socket, first: Buffer, method: string, absUrl: string) {
    let buf = first;
    const more = () => new Promise<Buffer>((res) => client.once("data", (d) => res(d as Buffer)));
    while (buf.indexOf("\r\n\r\n") < 0) buf = Buffer.concat([buf, await more()]);
    const headEnd = buf.indexOf("\r\n\r\n");
    const headers: Record<string, string> = {};
    for (const l of buf.toString("latin1", 0, headEnd).split("\r\n").slice(1)) {
      const i = l.indexOf(":");
      if (i > 0) headers[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
    }
    const clen = Number(headers["content-length"] ?? 0);
    let body = buf.subarray(headEnd + 4);
    while (body.length < clen) body = Buffer.concat([body, await more()]);

    const url = new URL(absUrl);
    const auth = await authorizeConn(moduleHops, canonicalHost(url.hostname), Number(url.port || 80));
    if (!auth.allow) { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); return; }
    const req: RequestView = { host: auth.host, method, path: url.pathname + url.search, headers, body: body.toString("latin1") };
    // Honor the terminate `domains` scope here too: a host we don't inspect gets
    // no content hooks, just forwarded upstream.
    const hooks = shouldInspect(auth.host) ? contentHops : [];
    const res = await runContent(hooks, hooks.length ? originates : true, req, "http");
    let status = Number(res.status);
    if (!Number.isInteger(status) || status < 200 || status > 599) status = 502;
    // Build the response headers through Headers(), which rejects CRLF in a value
    // — a hook header carrying "\r\n" must not split the response and smuggle
    // headers/body. Fail closed on an invalid header, matching the HTTPS path.
    let hdr = "";
    try {
      const h = new Headers(res.headers);
      for (const k of ["content-length", "content-encoding", "transfer-encoding"]) h.delete(k);
      for (const [k, v] of h) hdr += `${k}: ${v}\r\n`;
    } catch {
      client.end("HTTP/1.1 502 Bad Gateway\r\n\r\n");
      return;
    }
    const bodyBuf = Buffer.from(res.body, "latin1");
    const head = Buffer.from(`HTTP/1.1 ${status}\r\n${hdr}Content-Length: ${bodyBuf.length}\r\nConnection: close\r\n\r\n`, "latin1");
    client.end(Buffer.concat([head, bodyBuf]));
  }

  if (existsSync(cfg.socket)) { try { unlinkSync(cfg.socket); } catch {} }
  const server = net.createServer((client) => {
    client.on("error", () => {});
    // A hook (e.g. authorize) that throws MUST NOT crash the engine — that would
    // kill all egress for the box after boot. Catch per connection: deny + close.
    client.once("data", async (buf) => {
      try {
        const line = buf.toString("latin1").split("\r\n")[0];
        const connect = line.match(/^CONNECT\s+([^:]+):(\d+)/i);
        if (connect) {
          const host = canonicalHost(connect[1]);
          // Policy keys on the CONNECT host — sent in PLAINTEXT before any TLS —
          // and the raw tunnel below dials that SAME host. So what policy inspects
          // and where the bytes actually go are one value: a client cannot get
          // host A approved and reach host B via a mismatched inner Host/SNI. This
          // runs for EVERY connection, so the domain policy holds even for hosts
          // we later pass through.
          //
          // Residual caveat: this does NOT re-inspect content inside a passed-
          // through tunnel (a pinned host, or a no-terminate chain). If an
          // allowlist ever includes a SHARED CDN front, a request fronted behind
          // it inside the tunnel is not re-checked. Fine for the intended case
          // (single-tenant API hosts); a real concern only for hyperscaler CDNs.
          const auth = await authorizeConn(moduleHops, host, Number(connect[2]));
          if (!auth.allow) { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); return; }
          client.write("HTTP/1.1 200 Connection Established\r\n\r\n");

          // Peek the client's ClientHello (buffered so we can replay it). An
          // Encrypted Client Hello hides the real SNI from the domain policy, so
          // reject it unless ALLOW_ECH.
          const hello = await firstChunk(client);
          if (!hello) { client.destroy(); return; }
          if (!ALLOW_ECH && helloHasECH(hello)) { client.destroy(); return; }

          const passthrough = PASSTHROUGH_ON_TLS_FAILURE && pinnedHosts.has(host);
          if (shouldInspect(host) && !passthrough) {
            // CONTENT tier: hand the client's TLS to the host's terminator (cert
            // for the ORIGINAL host, the box's SNI). Replay the buffered hello and
            // watch the client's first record after our ServerHello: a TLS alert
            // (record type 0x15) means it rejected our leaf (pinning). Failing
            // closed, we log the refusal so the operator knows why a host went
            // dark; with PASSTHROUGH_ON_TLS_FAILURE we would instead learn the
            // host so the NEXT connection tunnels through un-intercepted.
            const up = net.connect(await terminatorFor(host), () => {
              up.write(hello);
              up.pipe(client);
              let firstSeen = false;
              client.on("data", (d: Buffer) => {
                if (!firstSeen) {
                  firstSeen = true;
                  if (d.length && d[0] === 0x15) {
                    if (PASSTHROUGH_ON_TLS_FAILURE) pinnedHosts.add(host);
                    else console.warn(`warning: refused ${host}: cannot intercept (client pins its cert) and inspection is required — failing closed`);
                  }
                }
                try { up.write(d); } catch {}
              });
            });
            const done = () => { client.destroy(); up.destroy(); };
            client.on("close", done);
            up.on("close", done);
            up.on("error", done);
            return;
          }
          // No terminate window, or a learned-pinned host: raw-tunnel the allowed
          // connection straight to the real host so a pinning client is not broken.
          const up = net.connect(auth.port, auth.host, () => { up.write(hello); client.pipe(up); up.pipe(client); });
          up.on("error", () => client.destroy());
          return;
        }
        const http = line.match(/^([A-Z]+)\s+(http:\/\/\S+)\s+HTTP\/1\.[01]/i);
        if (http) { await handlePlainHTTP(client, buf, http[1], http[2]); return; }
        client.end("HTTP/1.1 501 Not Implemented\r\n\r\n");
      } catch (e) {
        // Deny + close on any per-connection error, but leave a trail: a silent
        // 403 here once hid an engine bug behind a bare "Forbidden".
        console.error(`warning: proxy connection error: ${(e as Error)?.message ?? e}`);
        try { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); } catch {}
      }
    });
  });
  await new Promise<void>((resolve) => server.listen(cfg.socket, resolve));
  return { stop: () => server.close() };
}

// CLI entry: `bun engine.ts <config.json>`
if (import.meta.main) {
  const path = process.argv[2];
  if (!path) { console.error("usage: bun engine.ts <config.json>"); process.exit(2); }
  const cfg = (await Bun.file(path).json()) as EngineConfig;
  await start(cfg);
  console.log(`proxy engine → ${cfg.socket} (${cfg.chain.length} hops)`);
}
