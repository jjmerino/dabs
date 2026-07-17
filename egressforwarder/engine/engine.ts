// The dabs proxy engine: one in-process Bun program per box that runs a recipe's
// egress policy and (optional) HTTP content chain. The box's egress is pointed
// at this engine's unix socket (HTTP-proxy protocol). Two independent layers:
//
//   - POLICY (protocol-agnostic): the engine checks the CONNECT host against the
//     recipe's allow/deny patterns, on the plaintext host:port, BEFORE any
//     tunnel — for every protocol. A denied host gets 403; an allowed host is
//     tunneled. This is not a hook: it is engine-native and cannot be skipped.
//   - CONTENT (HTTP/1.1 only): inside a `tls: terminate` … `tls: originate`
//     window, the engine terminates TLS with a CA the box trusts and runs each
//     module hook's four verbs on the DECRYPTED HTTP/1.1 stream — streaming,
//     never buffering. Non-HTTP traffic inside a terminate window (h2, a raw
//     protocol) is re-originated untouched (a transparent tunnel), so
//     terminating a domain never costs the box its other protocols to it.
//
// The no-buffering law: request and response bodies flow through chunk by chunk,
// so a stream stays a stream and the wire framing/timing is preserved. A hook
// that needs whole-body context accumulates it ITSELF across chunks.
//
// The four content verbs (all fields plain JSON/Buffer; a hook never touches a
// socket), each given a per-request scratch `ctx`:
//   onRequest({host,port,method,path,headers}, ctx)
//     → nothing / {headers}                    edit request headers, continue
//     → {action:"respond", status?, headers?, body?}   answer here, break the chain
//     → {action:"deny", status?, headers?, body?}      refuse
//   onRequestChunk(chunk|null, ctx)  → Buffer|string|null|void
//     each request body chunk (null = EOF/flush). Return bytes to REPLACE the
//     chunk, or nothing to pass it through unchanged.
//   onResponse({host,status,headers}, ctx)  → nothing / {status?, headers?}
//   onResponseChunk(chunk|null, ctx)  → Buffer|string|null|void
//     each response body chunk (null = EOF/flush); transform or observe.
//
// Request hooks run chain order (box→internet); response hooks run REVERSE
// (internet→box), so a box-side hook is the last to touch a response.

import * as net from "node:net";
import * as tls from "node:tls";
import * as http from "node:http";
import * as https from "node:https";
import { existsSync, mkdirSync, unlinkSync, copyFileSync } from "node:fs";

type Json = Record<string, unknown>;

interface RequestHead { host: string; port: number; method: string; path: string; headers: Record<string, string>; }
interface ResponseHead { host: string; status: number; headers: Record<string, string>; }
type ChunkResult = Buffer | string | null | undefined | void;
interface Handler {
  onRequest?(head: RequestHead, ctx: Json): Promise<Json | void> | Json | void;
  onRequestChunk?(chunk: Buffer | null, ctx: Json): Promise<ChunkResult> | ChunkResult;
  onResponse?(head: ResponseHead, ctx: Json): Promise<Json | void> | Json | void;
  onResponseChunk?(chunk: Buffer | null, ctx: Json): Promise<ChunkResult> | ChunkResult;
}
interface HopConfig { name?: string; tls?: string; domains?: string[]; module?: string; config?: Json; }
interface EngineConfig { socket: string; caDir: string; allow?: string[]; deny?: string[]; chain: HopConfig[]; }

interface Hop { name: string; kind: "tls-terminate" | "tls-originate" | "module"; handler?: Handler; domains?: string[]; }

// --- certs: a CA (minted once) and a per-host leaf signed by it, cached. -----

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

// --- policy: the engine-native CONNECT gate. --------------------------------

// canonicalHost normalizes a host for policy: DNS names are case-insensitive and
// a trailing dot is the same server, so lowercase and strip it — otherwise
// "WWW.X.COM" or "www.x.com." would slip past a deny of "www.x.com".
function canonicalHost(host: string): string {
  return host.toLowerCase().replace(/\.+$/, "");
}

// matchPattern tests one canonical host against one pattern: `*` (all),
// `*.example.com` (any subdomain, not the apex), or an exact hostname.
function matchPattern(pattern: string, host: string): boolean {
  const p = pattern.toLowerCase();
  if (p === "*") return true;
  if (p.startsWith("*.")) return host.endsWith(p.slice(1)); // ".example.com" — subdomains only
  return host === p;
}

// compilePolicy turns allow/deny lists into a host→allowed predicate. Allow
// default-denies the rest; deny default-allows it; neither means all allowed.
// The recipe validator guarantees allow and deny are not both set.
function compilePolicy(allow: string[], deny: string[]): (host: string) => boolean {
  if (allow.length) return (host) => allow.some((p) => matchPattern(p, host));
  if (deny.length) return (host) => !deny.some((p) => matchPattern(p, host));
  return () => true;
}

// --- chain: load module hooks at boot; classify tls boundary directives. ----
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
    // a silent pass-through — no hooks — while a bad default export fails loudly.
    // Fail closed here too: reject at boot rather than open egress.
    if (!handler || typeof handler !== "object") throw new Error(`proxy module ${h.module}: factory must return a handler object, got ${handler === null ? "null" : typeof handler}`);
    hops.push({ name: h.name ?? h.module, kind: "module", handler: handler as Handler });
  }
  return hops;
}

// Classify the chain. contentHops is the subset of module hops inside a
// terminate…originate window — only those see decrypted HTTP content. Report
// whether the chain terminates (needs a CA) and originates (forwards upstream),
// and the terminate window's domain scope.
function classify(hops: Hop[]) {
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
    if (inWindow) contentHops.push(h);
    else if (h.handler?.onRequest || h.handler?.onResponse || h.handler?.onRequestChunk || h.handler?.onResponseChunk) {
      // A hook with content verbs but no window will never run. Say so at boot.
      console.warn(`warning: proxy hook "${h.name}" is OUTSIDE a "tls: terminate" window — its content verbs will NOT run. Add "tls: terminate" before it to inspect content.`);
    }
  }
  return { contentHops, terminates, originates, terminateDomains };
}

// Hooks are user code, so bound every call: a hung hook must not stall the box's
// egress forever. On timeout the promise rejects and the caller turns it into a
// clean refusal, never a hang. The budget assumes a LOCAL hook (a lookup, a
// string swap); a hook doing its own network I/O will not fit this contract.
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

// ALLOW_ECH: Encrypted Client Hello hides the real SNI, so we cannot verify the
// destination a connection actually targets — a hole in the domain policy. false
// (the default) refuses any ClientHello carrying the ECH extension.
const ALLOW_ECH = false;

// PASSTHROUGH_ON_TLS_FAILURE: a client that PINS its cert rejects our MITM leaf.
// A "tls: terminate" window is a declaration that the traffic MUST be inspected,
// so a host we cannot intercept is REFUSED, not waved through: passthrough on a
// broker would let a credential the host was meant to swap out land inside the
// box. false (the default) fails closed. A single constant so a future
// recipe-schema knob can wire it per box without touching this logic.
const PASSTHROUGH_ON_TLS_FAILURE = false;

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

// --- streaming reader: pull bytes off a duplex as they arrive. ---------------
// A promise-based pull over a socket's data/end/error, so the HTTP parser can
// `await` the next bytes without buffering the whole stream.
function makePull(sock: NodeJS.ReadableStream): () => Promise<Buffer | null> {
  const queue: Buffer[] = [];
  let waiting: ((b: Buffer | null) => void) | null = null;
  let ended = false;
  let errored: Error | null = null;
  sock.on("data", (d: Buffer) => {
    if (waiting) { const w = waiting; waiting = null; w(d); }
    else queue.push(d);
  });
  sock.on("end", () => { ended = true; if (waiting) { const w = waiting; waiting = null; w(null); } });
  sock.on("error", (e: Error) => { errored = e; if (waiting) { const w = waiting; waiting = null; w(null); } });
  return () => new Promise<Buffer | null>((resolve) => {
    if (queue.length) return resolve(queue.shift()!);
    if (ended || errored) return resolve(null);
    waiting = resolve;
  });
}

// BufReader gives the HTTP parser readUntil / readSome / readExact over a pull,
// holding only the current unconsumed bytes (bounded by one chunk plus a head).
class BufReader {
  private buf = Buffer.alloc(0);
  private done = false;
  constructor(private pull: () => Promise<Buffer | null>, initial: Buffer) { this.buf = initial; }
  private async more(): Promise<boolean> {
    if (this.done) return false;
    const d = await this.pull();
    if (d === null) { this.done = true; return false; }
    this.buf = this.buf.length ? Buffer.concat([this.buf, d]) : d;
    return true;
  }
  async readUntil(delim: string): Promise<Buffer | null> {
    for (;;) {
      const i = this.buf.indexOf(delim);
      if (i >= 0) { const out = this.buf.subarray(0, i); this.buf = this.buf.subarray(i + delim.length); return out; }
      if (!(await this.more())) return this.buf.length ? this.take() : null;
    }
  }
  async readSome(max: number): Promise<Buffer> {
    if (!this.buf.length && !(await this.more())) return Buffer.alloc(0);
    const n = Math.min(max, this.buf.length);
    const out = this.buf.subarray(0, n);
    this.buf = this.buf.subarray(n);
    return out;
  }
  async readExact(n: number): Promise<Buffer> {
    while (this.buf.length < n) { if (!(await this.more())) break; }
    return this.readSome(n);
  }
  private take(): Buffer { const o = this.buf; this.buf = Buffer.alloc(0); return o; }
}

// parseHeadLines splits a raw header block into the start line and a lowercased
// header map. Duplicate headers keep the last value (rare on requests).
function parseHeadLines(raw: Buffer): { start: string; headers: Record<string, string> } {
  const lines = raw.toString("latin1").split("\r\n");
  const start = lines[0] ?? "";
  const headers: Record<string, string> = {};
  for (const l of lines.slice(1)) {
    const i = l.indexOf(":");
    if (i > 0) headers[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
  }
  return { start, headers };
}

// bodyChunks yields the request/response body as a stream, decoding the wire
// framing (content-length or chunked) but NOT buffering the whole body.
async function* bodyChunks(reader: BufReader, headers: Record<string, string>, method?: string): AsyncGenerator<Buffer> {
  if (method === "GET" || method === "HEAD") return;
  const te = (headers["transfer-encoding"] ?? "").toLowerCase();
  if (te.includes("chunked")) {
    for (;;) {
      const line = await reader.readUntil("\r\n");
      if (line === null) return;
      const size = parseInt(line.toString("latin1").trim().split(";")[0], 16);
      if (!Number.isFinite(size) || size <= 0) { await reader.readUntil("\r\n"); return; } // 0-chunk (or garbage): end
      let rem = size;
      while (rem > 0) { const c = await reader.readSome(rem); if (!c.length) return; rem -= c.length; yield c; }
      await reader.readExact(2); // trailing CRLF
    }
  }
  const clen = Number(headers["content-length"] ?? 0);
  if (clen > 0) {
    let rem = clen;
    while (rem > 0) { const c = await reader.readSome(rem); if (!c.length) return; rem -= c.length; yield c; }
  }
  // No framing header and not chunked: no body (a request without content-length
  // is bodyless; a response body with neither is handled by connection close on
  // the upstream side, which node's http client surfaces as stream end).
}

function asBuf(v: ChunkResult): Buffer | null {
  if (v == null) return null;
  return Buffer.isBuffer(v) ? v : Buffer.from(String(v), "latin1");
}

// A per-request bundle: the content hops with a fresh scratch ctx each.
interface Traversal { hop: Hop; ctx: Json; }

// applyDataChunk folds a body chunk through a sequence of hooks (request order,
// or response REVERSE order). Each hook returns replacement bytes or nothing
// (pass through). An empty result drops the chunk.
async function applyDataChunk(seq: Traversal[], verb: "onRequestChunk" | "onResponseChunk", chunk: Buffer): Promise<Buffer> {
  let cur = chunk;
  for (const t of seq) {
    const fn = t.hop.handler?.[verb];
    if (!fn) continue;
    const r = asBuf(await callHook(fn.call(t.hop.handler, cur, t.ctx)));
    if (r !== null) cur = r;
  }
  return cur;
}

// applyEOF flushes every hook's end-of-body bytes (the null chunk). Returns the
// concatenated trailing bytes to emit before closing the body.
async function applyEOF(seq: Traversal[], verb: "onRequestChunk" | "onResponseChunk"): Promise<Buffer> {
  const extra: Buffer[] = [];
  for (const t of seq) {
    const fn = t.hop.handler?.[verb];
    if (!fn) continue;
    const r = asBuf(await callHook(fn.call(t.hop.handler, null, t.ctx)));
    if (r && r.length) extra.push(r);
  }
  return extra.length ? Buffer.concat(extra) : Buffer.alloc(0);
}

// chunkEncode wraps bytes as one HTTP/1.1 chunked-transfer chunk.
function chunkEncode(b: Buffer): Buffer {
  return Buffer.concat([Buffer.from(`${b.length.toString(16)}\r\n`, "latin1"), b, Buffer.from("\r\n", "latin1")]);
}

// serializeResponseHead builds the status line + header block for the box, using
// chunked transfer (the body length is unknown until hooks finish streaming).
// Header VALUES are validated against CRLF — a hook value carrying "\r\n" must
// not split the response and smuggle headers. Throws on an invalid header
// (the caller fails closed with a 502).
function serializeResponseHead(status: number, headers: Record<string, string | string[]>): Buffer {
  let out = `HTTP/1.1 ${status}\r\n`;
  for (const [k, v] of Object.entries(headers)) {
    const lk = k.toLowerCase();
    if (lk === "content-length" || lk === "transfer-encoding" || lk === "connection") continue;
    for (const one of Array.isArray(v) ? v : [v]) {
      if (/[\r\n]/.test(one)) throw new Error(`header ${k} carries CRLF`);
      out += `${k}: ${one}\r\n`;
    }
  }
  out += "Transfer-Encoding: chunked\r\nConnection: close\r\n\r\n";
  return Buffer.from(out, "latin1");
}

// --- CONTENT tier: one HTTP/1.1 request over a decrypted (or plain) duplex. ---
// Reads the request head, runs onRequest in order (a hook may respond/deny and
// break the chain), streams the request body upstream through onRequestChunk,
// then streams the upstream response back through onResponse/onResponseChunk in
// REVERSE. forwardToUpstream is false for a terminal chain (no `tls: originate`):
// then only a hook that responds can produce an answer.
async function serveHTTP(
  client: net.Socket | tls.TLSSocket,
  contentHops: Hop[],
  forwardToUpstream: boolean,
  host: string,
  port: number,
  scheme: "http" | "https",
  initial: Buffer,
): Promise<void> {
  const reader = new BufReader(makePull(client), initial);
  const rawHead = await reader.readUntil("\r\n\r\n");
  if (rawHead === null) { client.destroy(); return; }
  const { start, headers } = parseHeadLines(rawHead);
  const m = start.match(/^([A-Z]+)\s+(\S+)\s+HTTP\/1\.[01]$/i);
  if (!m) { client.end("HTTP/1.1 400 Bad Request\r\n\r\n"); return; }
  const method = m[1].toUpperCase();
  // For a plain forward-proxy request the target is an absolute URL; for a
  // terminated TLS request it is an origin-form path.
  const path = scheme === "http" && /^https?:\/\//i.test(m[2]) ? new URL(m[2]).pathname + new URL(m[2]).search : m[2];

  const seq: Traversal[] = contentHops.map((hop) => ({ hop, ctx: {} }));
  const reqHead: RequestHead = { host, port, method, path, headers };

  // onRequest in order. A respond/deny short-circuits; the hops the request
  // already passed (including the responder) unwind the response in reverse.
  let traversed = 0;
  let localResponse: { status: number; headers: Record<string, string>; body: Buffer } | null = null;
  for (let i = 0; i < seq.length; i++) {
    traversed = i + 1;
    const fn = seq[i].hop.handler?.onRequest;
    if (!fn) continue;
    let verdict: Json | void;
    try { verdict = await callHook(fn.call(seq[i].hop.handler, reqHead, seq[i].ctx)); }
    catch (e) { client.end("HTTP/1.1 502 Bad Gateway\r\n\r\n"); console.error(`warning: onRequest hook failed for ${host}: ${(e as Error)?.message ?? e}`); return; }
    if (verdict?.action === "respond" || verdict?.action === "deny") {
      const dflt = verdict.action === "deny" ? 403 : 200;
      localResponse = {
        status: Number(verdict.status ?? dflt),
        headers: (verdict.headers as Record<string, string>) ?? {},
        body: asBuf(verdict.body as ChunkResult) ?? Buffer.alloc(0),
      };
      break;
    }
    if (verdict?.headers) Object.assign(reqHead.headers, verdict.headers as Record<string, string>);
  }

  const respSeq = seq.slice(0, traversed).reverse(); // response unwinds in reverse

  if (localResponse) {
    // Drain any request body the client is still sending, so the socket is not
    // left half-read (the box's client waits on us, not the other way round).
    // A local responder answers without upstream, so we discard it.
    await drain(bodyChunks(reader, reqHead.headers, method));
    await writeResponse(client, { status: localResponse.status, headers: localResponse.headers }, singleChunk(localResponse.body), respSeq, host);
    return;
  }

  if (!forwardToUpstream) {
    await drain(bodyChunks(reader, reqHead.headers, method));
    await writeResponse(client, { status: 502, headers: {} }, singleChunk(Buffer.from("request reached the last hook but the chain is terminal — add a hook that responds, or a `tls: originate` to reach the upstream", "latin1")), respSeq, host);
    return;
  }

  // Forward upstream, streaming the request body through onRequestChunk.
  const isDefaultPort = (scheme === "https" && port === 443) || (scheme === "http" && port === 80);
  const authority = isDefaultPort ? host : `${host}:${port}`;
  const outHeaders: Record<string, string> = {};
  for (const [k, v] of Object.entries(reqHead.headers)) {
    const lk = k.toLowerCase();
    if (lk === "host" || lk === "proxy-connection" || lk === "content-length" || lk === "connection") continue;
    outHeaders[k] = v;
  }
  outHeaders["host"] = authority;
  outHeaders["connection"] = "close";

  const mod = scheme === "https" ? https : http;
  const upReq = mod.request({ host, port, method, path, headers: outHeaders, servername: scheme === "https" ? host : undefined, rejectUnauthorized: true });

  const upResPromise = new Promise<http.IncomingMessage>((resolve, reject) => {
    upReq.on("response", resolve);
    upReq.on("error", reject);
  });

  // Pump the request body up, one transformed chunk at a time.
  try {
    for await (const chunk of bodyChunks(reader, reqHead.headers, method)) {
      const out = await applyDataChunk(seq, "onRequestChunk", chunk);
      if (out.length) upReq.write(out);
    }
    const tail = await applyEOF(seq, "onRequestChunk");
    if (tail.length) upReq.write(tail);
    upReq.end();
  } catch (e) {
    upReq.destroy();
    client.end("HTTP/1.1 502 Bad Gateway\r\n\r\n");
    console.error(`warning: request stream failed for ${host}: ${(e as Error)?.message ?? e}`);
    return;
  }

  let upRes: http.IncomingMessage;
  try { upRes = await upResPromise; }
  catch (e) { client.end("HTTP/1.1 502 Bad Gateway\r\n\r\n"); console.error(`warning: upstream error for ${host}: ${(e as Error)?.message ?? e}`); return; }

  const respHead: ResponseHead = { host, status: upRes.statusCode ?? 502, headers: {} };
  for (const [k, v] of Object.entries(upRes.headers)) respHead.headers[k] = Array.isArray(v) ? v.join(", ") : String(v ?? "");
  await writeResponse(client, respHead, upRes as unknown as AsyncIterable<Buffer>, respSeq, host, upRes.headers);
}

// singleChunk adapts one Buffer into a body stream (for a local respond).
async function* singleChunk(b: Buffer): AsyncGenerator<Buffer> { if (b.length) yield b; }
async function drain(gen: AsyncGenerator<Buffer>) { try { for await (const _ of gen) { /* discard */ } } catch { /* ignore */ } }

// writeResponse runs onResponse (reverse) over the head, writes the head to the
// box with chunked framing, then streams the body through onResponseChunk
// (reverse), chunk-encoding each result — never buffering the body.
async function writeResponse(
  client: net.Socket | tls.TLSSocket,
  head: { status: number; headers: Record<string, string> },
  body: AsyncIterable<Buffer>,
  respSeq: Traversal[],
  host: string,
  rawHeaders?: http.IncomingHttpHeaders,
): Promise<void> {
  const resHead: ResponseHead = { host, status: head.status, headers: { ...head.headers } };
  for (const t of respSeq) {
    const fn = t.hop.handler?.onResponse;
    if (!fn) continue;
    try {
      const r = await callHook(fn.call(t.hop.handler, resHead, t.ctx)) as Json | undefined;
      if (r?.status != null) resHead.status = Number(r.status);
      if (r?.headers) Object.assign(resHead.headers, r.headers as Record<string, string>);
    } catch (e) { console.error(`warning: onResponse hook failed for ${host}: ${(e as Error)?.message ?? e}`); }
  }
  // set-cookie may be multi-valued: pull the raw array through so each cookie is
  // emitted as its own header line.
  const outHeaders: Record<string, string | string[]> = { ...resHead.headers };
  if (rawHeaders && Array.isArray(rawHeaders["set-cookie"])) outHeaders["set-cookie"] = rawHeaders["set-cookie"] as string[];
  let status = Number(resHead.status);
  if (!Number.isInteger(status) || status < 200 || status > 599) status = 502;
  try { client.write(serializeResponseHead(status, outHeaders)); }
  catch { client.end("HTTP/1.1 502 Bad Gateway\r\n\r\n"); return; }
  try {
    for await (const chunk of body) {
      const out = await applyDataChunk(respSeq, "onResponseChunk", Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
      if (out.length) client.write(chunkEncode(out));
    }
    const tail = await applyEOF(respSeq, "onResponseChunk");
    if (tail.length) client.write(chunkEncode(tail));
    client.write(Buffer.from("0\r\n\r\n", "latin1"));
    client.end();
  } catch (e) {
    console.error(`warning: response stream failed for ${host}: ${(e as Error)?.message ?? e}`);
    client.destroy();
  }
}

// --- boot -------------------------------------------------------------------
export async function start(cfg: EngineConfig): Promise<{ stop: () => void }> {
  const hops = await resolveChain(cfg.chain);
  const { contentHops, terminates, originates, terminateDomains } = classify(hops);
  const policyAllows = compilePolicy(cfg.allow ?? [], cfg.deny ?? []);

  // shouldInspect decides whether THIS host's TLS is terminated and its content
  // inspected. With no `domains` list a terminate window covers every host; a
  // list scopes it (exact host or a subdomain), so traffic we don't care about
  // passes through un-decrypted.
  function shouldInspect(host: string): boolean {
    if (!terminates) return false;
    if (!terminateDomains) return true;
    return terminateDomains.some((d) => host === d || host.endsWith("." + d));
  }
  // Always mint the CA: dabs mounts ca.crt into every proxy box (the mount origin
  // must exist), and a chain that gains a `tls: terminate` later needs it anyway.
  ensureCA(cfg.caDir);

  const pinnedHosts = new Set<string>(); // clients that rejected our leaf (pinning)

  // A TLS terminator per host:port — a node:tls server bound to a unix socket,
  // presenting the host's leaf. The CONNECT handler pipes the client's raw TLS
  // bytes into it; the server hands back a DECRYPTED duplex (onDecrypted), which
  // is either served as HTTP/1.1 or re-originated untouched for any other
  // protocol. Single-flighted like leafFor so concurrent first-contacts share
  // one server. node:tls (not Bun's HTTP server) is what lets a non-HTTP stream
  // pass through — terminating a domain never costs the box its other protocols.
  const terminators = new Map<string, Promise<string>>();
  function terminatorFor(host: string, port: number): Promise<string> {
    const key = `${host}:${port}`;
    const existing = terminators.get(key);
    if (existing) return existing;
    const p = makeTerminator(host, port);
    p.catch(() => terminators.delete(key));
    terminators.set(key, p);
    return p;
  }
  async function makeTerminator(host: string, port: number): Promise<string> {
    const leaf = await leafFor(cfg.caDir, host);
    const safe = `${host}_${port}`.replace(/[^a-zA-Z0-9.-]/g, "_");
    const base = safe.length <= 40 ? safe : `${safe.slice(0, 24)}-${Bun.hash(safe).toString(36)}`;
    const sock = `${cfg.caDir}/term-${base}.sock`;
    if (existsSync(sock)) { try { unlinkSync(sock); } catch {} }
    const srv = tls.createServer({ cert: leaf.cert, key: leaf.key, ALPNProtocols: ["http/1.1"] }, (tlsSock) => {
      onDecrypted(tlsSock, host, port).catch((e) => { console.error(`warning: proxy content error for ${host}: ${e?.message ?? e}`); tlsSock.destroy(); });
    });
    // A client that PINS its cert rejects our leaf → the handshake fails here.
    srv.on("tlsClientError", () => {
      if (PASSTHROUGH_ON_TLS_FAILURE) pinnedHosts.add(host);
      else console.warn(`warning: refused ${host}: cannot intercept (client pins its cert) and inspection is required — failing closed`);
    });
    await new Promise<void>((resolve) => srv.listen(sock, resolve));
    return sock;
  }
  // onDecrypted receives a decrypted client duplex. HTTP/1.1 → the content path;
  // the h2 preface or any other protocol → re-originate a fresh TLS to the real
  // upstream and tunnel the decrypted bytes untouched.
  async function onDecrypted(tlsSock: tls.TLSSocket, host: string, port: number) {
    const first = await firstChunk(tlsSock as unknown as net.Socket);
    if (first === null) { tlsSock.destroy(); return; }
    const isHTTP1 = (tlsSock.alpnProtocol === "http/1.1" || tlsSock.alpnProtocol === false) && looksLikeHTTP1(first);
    if (isHTTP1) {
      await serveHTTP(tlsSock as unknown as net.Socket, contentHops, originates, host, port, "https", first);
      return;
    }
    const up = tls.connect({ host, port, servername: host }, () => {
      up.write(first);
      up.pipe(tlsSock);
      tlsSock.pipe(up);
    });
    const done = () => { tlsSock.destroy(); up.destroy(); };
    tlsSock.on("close", done); up.on("close", done); up.on("error", done);
  }

  if (existsSync(cfg.socket)) { try { unlinkSync(cfg.socket); } catch {} }
  const server = net.createServer((client) => {
    client.on("error", () => {});
    client.once("data", async (buf) => {
      try {
        const line = buf.toString("latin1").split("\r\n")[0];
        const connect = line.match(/^CONNECT\s+([^:]+):(\d+)/i);
        if (connect) {
          const host = canonicalHost(connect[1]);
          const port = Number(connect[2]);
          // POLICY: the engine-native CONNECT gate, on the plaintext host, before
          // any tunnel — for every protocol. A denied host never gets a tunnel.
          if (!policyAllows(host)) { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); return; }
          client.write("HTTP/1.1 200 Connection Established\r\n\r\n");

          const hello = await firstChunk(client);
          if (!hello) { client.destroy(); return; }
          if (!ALLOW_ECH && helloHasECH(hello)) { client.destroy(); return; } // ECH hides the SNI

          const passthrough = PASSTHROUGH_ON_TLS_FAILURE && pinnedHosts.has(host);
          if (shouldInspect(host) && !passthrough) {
            // Pipe the client's raw TLS into the host's terminator, replaying the
            // buffered ClientHello. The terminator decrypts and calls onDecrypted.
            const inner = net.connect(await terminatorFor(host, port), () => {
              inner.write(hello);
              client.pipe(inner);
              inner.pipe(client);
            });
            const done = () => { client.destroy(); inner.destroy(); };
            client.on("close", done); inner.on("close", done); inner.on("error", done);
            return;
          }
          // No terminate window (or a learned-pinned host): raw-tunnel the allowed
          // connection straight to the real host — a pinning client is not broken,
          // and any protocol reaches an allowed domain.
          const up = net.connect(port, host, () => { up.write(hello); client.pipe(up); up.pipe(client); });
          up.on("error", () => client.destroy());
          return;
        }
        const httpReq = line.match(/^([A-Z]+)\s+(http:\/\/\S+)\s+HTTP\/1\.[01]/i);
        if (httpReq) {
          // A plain forward-proxy request (`GET http://host/path`) — already
          // plaintext. Gate the host, then serve it through the content hops.
          const url = new URL(httpReq[2]);
          const host = canonicalHost(url.hostname);
          const port = Number(url.port || 80);
          if (!policyAllows(host)) { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); return; }
          const hooks = shouldInspect(host) ? contentHops : [];
          await serveHTTP(client, hooks, hooks.length ? originates : true, host, port, "http", buf);
          return;
        }
        client.end("HTTP/1.1 501 Not Implemented\r\n\r\n");
      } catch (e) {
        console.error(`warning: proxy connection error: ${(e as Error)?.message ?? e}`);
        try { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); } catch {}
      }
    });
  });
  await new Promise<void>((resolve) => server.listen(cfg.socket, resolve));
  return { stop: () => server.close() };
}

// looksLikeHTTP1 reports whether the first decrypted bytes begin an HTTP/1.x
// request line (a method token then an HTTP/1 version) — and are NOT the HTTP/2
// connection preface, which also starts with an uppercase token.
function looksLikeHTTP1(buf: Buffer): boolean {
  const head = buf.toString("latin1", 0, Math.min(buf.length, 8192));
  if (head.startsWith("PRI * HTTP/2")) return false; // h2 preface
  const line = head.split("\r\n")[0];
  return /^[A-Z]+\s+\S+\s+HTTP\/1\.[01]$/.test(line);
}

// CLI entry: `bun engine.ts <config.json>`
if (import.meta.main) {
  const path = process.argv[2];
  if (!path) { console.error("usage: bun engine.ts <config.json>"); process.exit(2); }
  const cfg = (await Bun.file(path).json()) as EngineConfig;
  await start(cfg);
  console.log(`proxy engine → ${cfg.socket} (${cfg.chain.length} hops, allow=${(cfg.allow ?? []).length} deny=${(cfg.deny ?? []).length})`);
}
