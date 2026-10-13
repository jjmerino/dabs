// The dabs proxy engine: one in-process Bun program per box that runs a recipe's
// egress policy and (optional) HTTP content chain. The box's egress is pointed
// at this engine's unix socket (HTTP-proxy protocol). Three layers:
//
//   - POLICY (protocol-agnostic): the engine checks the CONNECT host against the
//     recipe's allow/deny patterns, on the plaintext host:port, BEFORE any
//     tunnel — for every protocol. A denied host gets 403; an allowed host is
//     tunneled. This is not a hook: it is engine-native and cannot be skipped.
//   - CONNECTION (protocol-agnostic): a module hop OUTSIDE any tls window acts
//     on the connection, not its content. After the static gate ALLOWS a
//     host:port, each such hop's `onConnect` may refuse it. Narrowing only: the
//     static gate has already run, so a hop is never asked about a host the
//     recipe denies, and a hook can never widen the recipe's policy.
//   - CONTENT (HTTP/1.1 only): inside a `tls: terminate` … `tls: originate`
//     window, the engine terminates TLS with a CA the box trusts and runs each
//     module hook's four verbs on the DECRYPTED HTTP/1.1 stream — streaming,
//     never buffering. Non-HTTP traffic inside a terminate window (h2, a raw
//     protocol) is re-originated untouched (a transparent tunnel), so
//     terminating a domain never costs the box its other protocols to it.
//
// Under all three, every connection the engine opens is to an address it has
// checked: no loopback, private, link-local or other local address, and none
// on a network of the host's interfaces (see the ADDRESS section).
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
// The connection verb, on a hop outside any tls window, with a per-connection
// scratch `ctx`:
//   onConnect({host,port}, ctx)
//     → nothing / {action:"allow"}   let the connection through
//     → {action:"deny", reason?}     refuse it (the box gets 403)
//
// Request hooks run chain order (box→internet); response hooks run REVERSE
// (internet→box), so a box-side hook is the last to touch a response.
// onConnect runs chain order, and the first deny wins.

import * as net from "node:net";
import * as tls from "node:tls";
import * as http from "node:http";
import * as https from "node:https";
import * as dns from "node:dns";
import * as os from "node:os";
import { existsSync, mkdirSync, unlinkSync, copyFileSync, appendFileSync } from "node:fs";

// logEvent appends a JSON line to the connection ledger (DABS_CONNECT_LOG). It is
// module-level so the streaming path (serveHTTP/writeResponse) can record an
// upstream response head or a stream error at a KNOWN path — otherwise a hung or
// failed request leaves no trace but a warning in a temp engine.log. start() wires
// it to the real sink; until then it is a no-op.
let logEvent: (o: object) => void = () => {};

type Json = Record<string, unknown>;

interface RequestHead { host: string; port: number; method: string; path: string; headers: Record<string, string>; }
interface ResponseHead { host: string; status: number; headers: Record<string, string>; }
interface ConnectTarget { host: string; port: number; }
type ChunkResult = Buffer | string | null | undefined | void;
interface Handler {
  onConnect?(target: ConnectTarget, ctx: Json): Promise<Json | void> | Json | void;
  onRequest?(head: RequestHead, ctx: Json): Promise<Json | void> | Json | void;
  onRequestChunk?(chunk: Buffer | null, ctx: Json): Promise<ChunkResult> | ChunkResult;
  onResponse?(head: ResponseHead, ctx: Json): Promise<Json | void> | Json | void;
  onResponseChunk?(chunk: Buffer | null, ctx: Json): Promise<ChunkResult> | ChunkResult;
}
interface HopConfig { name?: string; tls?: string; domains?: string[]; failOpen?: boolean; module?: string; config?: Json; }
// allowLocal: addresses or CIDR prefixes the engine may dial although
// refusedAddress refuses them (see the ADDRESS section). Absent, the engine
// reads DABS_EGRESS_ALLOW_LOCAL. resolve replaces the system resolver; only a
// caller of start() can pass it, since JSON cannot carry a function.
interface EngineConfig { socket: string; caDir: string; allow?: string[]; deny?: string[]; chain: HopConfig[]; allowLocal?: string[]; resolve?: Resolve; }

interface Hop { name: string; kind: "tls-terminate" | "tls-originate" | "module"; handler?: Handler; domains?: string[]; failOpen?: boolean; }

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
// "WWW.X.COM" or "www.x.com." would slip past a deny of "www.x.com". An IPv6
// literal loses its brackets (`[::1]` from a CONNECT authority or a URL's
// hostname): the host is bare everywhere after this, and serveHTTP puts the
// brackets back in the Host header.
function canonicalHost(host: string): string {
  return host.toLowerCase().replace(/^\[(.*)\]$/, "$1").replace(/\.+$/, "");
}

// matchHost tests one canonical host against one pattern, with ONE meaning of a
// pattern shared by every policy surface (the CONNECT gate AND the terminate
// scope), so a pattern never means two things depending on where it is written:
//   `*`              → all
//   `*.example.com`  → subdomains only, NOT the apex
//   `example.com`    → the apex AND every subdomain
export function matchHost(pattern: string, host: string): boolean {
  const p = pattern.toLowerCase();
  if (p === "*") return true;
  if (p.startsWith("*.")) return host.endsWith(p.slice(1)); // ".example.com" — subdomains only
  return host === p || host.endsWith("." + p);              // bare — apex + subdomains
}

// compilePolicy turns allow/deny lists into a host→allowed predicate. Allow
// default-denies the rest; deny default-allows it; neither means all allowed.
// The recipe validator guarantees allow and deny are not both set.
function compilePolicy(allow: string[], deny: string[]): (host: string) => boolean {
  if (allow.length) return (host) => allow.some((p) => matchHost(p, host));
  if (deny.length) return (host) => !deny.some((p) => matchHost(p, host));
  return () => true;
}

// --- ADDRESS: the engine dials no local or private address. ------------------
//
// Every connection the engine opens for a box is checked here first:
// resolveChecked resolves the name once and checks EVERY address with
// refusedAddress, and connectTo connects to one of those addresses, never to
// the name. A name with one public and one refused address is refused. A
// second lookup could answer differently from the one that was checked, so
// there is none.
//
// The check runs when a connection is opened, after the gate: a request a
// hook answers itself opens none, so it is never refused for its address. A
// CONNECT that will be tunneled is checked before its 200, so the box can be
// told why with a 403.
//
// An operator can open given addresses with `allowLocal` in the config or
// DABS_EGRESS_ALLOW_LOCAL (comma-separated addresses or CIDR prefixes) in the
// engine's environment. dabs's own tests use it for upstreams they run on
// loopback. A box cannot set either.

// An IP address as bytes: 4 for IPv4, 16 for IPv6.
type IP = number[];
interface Prefix { ip: IP; bits: number; }

// ipv6Groups parses an IPv6 address (no brackets, no zone) into its eight
// 16-bit groups, or null when it is not one. The URL parser does the
// validating: it accepts every textual form (`::` compression, a dotted IPv4
// tail, upper case) and serializes all of them as compressed hex.
function ipv6Groups(addr: string): number[] | null {
  let c: string;
  try { c = new URL(`http://[${addr}]/`).hostname.slice(1, -1); } catch { return null; }
  const halves = c.split("::");
  if (halves.length > 2) return null;
  const head = halves[0] ? halves[0].split(":") : [];
  const tail = halves.length === 2 && halves[1] ? halves[1].split(":") : [];
  const fill = halves.length === 2 ? 8 - head.length - tail.length : 0;
  const groups = [...head, ...Array(fill).fill("0"), ...tail].map((g) => parseInt(g, 16));
  return groups.length === 8 && groups.every((g) => g >= 0 && g <= 0xffff) ? groups : null;
}

// parseIP reads an IPv4 or IPv6 address in its standard spelling, with or
// without brackets, dropping an IPv6 zone. Anything else is null; see
// literalAddress for the other IPv4 spellings.
export function parseIP(s: string): IP | null {
  const h = s.replace(/^\[(.*)\]$/, "$1").replace(/%.*$/, "");
  switch (net.isIP(h)) {
    case 4: return h.split(".").map(Number);
    case 6: { const g = ipv6Groups(h); return g ? g.flatMap((x) => [x >> 8, x & 0xff]) : null; }
    default: return null;
  }
}

// literalAddress returns the address a host names when it is an IP literal in
// any spelling the URL parser accepts (`127.1`, `2130706433`, `0x7f.0.0.1`,
// `[::ffff:7f00:1]`), or null when it is a name.
export function literalAddress(host: string): string | null {
  const h = host.replace(/^\[(.*)\]$/, "$1").replace(/%.*$/, "");
  if (net.isIP(h)) return h;
  if (h.includes(":")) return null;
  let norm: string;
  try { norm = new URL(`http://${h}/`).hostname; } catch { return null; }
  return net.isIPv4(norm) ? norm : null;
}

function formatIP(ip: IP): string {
  if (ip.length === 4) return ip.join(".");
  const groups: string[] = [];
  for (let i = 0; i < 16; i += 2) groups.push(((ip[i] << 8) | ip[i + 1]).toString(16));
  return new URL(`http://[${groups.join(":")}]/`).hostname.slice(1, -1);
}

export function parsePrefix(s: string): Prefix | null {
  const [addr, len, ...rest] = s.trim().split("/");
  if (rest.length) return null;
  const ip = parseIP(addr);
  if (!ip) return null;
  const max = ip.length * 8;
  if (len === undefined) return { ip, bits: max };
  if (!/^\d+$/.test(len) || Number(len) > max) return null;
  return { ip, bits: Number(len) };
}

function inPrefix(p: Prefix, ip: IP): boolean {
  if (p.ip.length !== ip.length) return false;
  for (let bit = 0; bit < p.bits; bit += 8) {
    const n = Math.min(8, p.bits - bit);
    const mask = (0xff << (8 - n)) & 0xff;
    if ((p.ip[bit / 8] & mask) !== (ip[bit / 8] & mask)) return false;
  }
  return true;
}

// unmap turns an IPv4-mapped IPv6 address (::ffff:a.b.c.d) into its IPv4 address.
function unmap(ip: IP): IP {
  if (ip.length === 16 && ip.slice(0, 10).every((b) => b === 0) && ip[10] === 0xff && ip[11] === 0xff) return ip.slice(12);
  return ip;
}

const prefix = (s: string): Prefix => parsePrefix(s)!;

// REFUSED_RANGES are the ranges the engine never dials, each with the words a
// refusal names it by.
const REFUSED_RANGES: [Prefix, string][] = [
  [prefix("0.0.0.0/8"), "an address of this network (0.0.0.0/8)"],
  [prefix("10.0.0.0/8"), "a private address (10.0.0.0/8)"],
  [prefix("100.64.0.0/10"), "a shared address (100.64.0.0/10, carrier NAT and tailnets)"],
  [prefix("127.0.0.0/8"), "a loopback address"],
  [prefix("169.254.0.0/16"), "a link-local address (169.254.0.0/16, cloud metadata among them)"],
  [prefix("172.16.0.0/12"), "a private address (172.16.0.0/12)"],
  [prefix("192.0.0.0/24"), "an IETF protocol address (192.0.0.0/24)"],
  [prefix("192.168.0.0/16"), "a private address (192.168.0.0/16)"],
  [prefix("198.18.0.0/15"), "a benchmarking address (198.18.0.0/15)"],
  [prefix("224.0.0.0/4"), "a multicast address"],
  [prefix("240.0.0.0/4"), "a reserved address (240.0.0.0/4, broadcast among them)"],
  [prefix("::/128"), "the unspecified address"],
  [prefix("::1/128"), "a loopback address"],
  [prefix("::/96"), "an IPv4-compatible address (::/96)"],
  [prefix("::ffff:0:0:0/96"), "an IPv4-translated address (::ffff:0:0:0/96)"],
  [prefix("64:ff9b:1::/48"), "a local-use NAT64 address (64:ff9b:1::/48)"],
  [prefix("100::/64"), "a discard address (100::/64)"],
  [prefix("2001::/32"), "a Teredo address (2001::/32)"],
  [prefix("fc00::/7"), "a unique local address (fc00::/7)"],
  [prefix("fe80::/10"), "a link-local address"],
  [prefix("fec0::/10"), "a site-local address (fec0::/10)"],
  [prefix("ff00::/8"), "a multicast address"],
];
// The IPv6 ranges that carry an IPv4 address, checked as that address.
const NAT64 = prefix("64:ff9b::/96");    // the last 4 bytes
const SIX_TO_FOUR = prefix("2002::/16"); // bytes 2 to 5

// hostNetworks lists the networks this host's interfaces are on, or null when
// they cannot be read.
export function hostNetworks(): Prefix[] | null {
  try {
    const out: Prefix[] = [];
    for (const list of Object.values(os.networkInterfaces())) {
      for (const a of list ?? []) {
        const p = a.cidr ? parsePrefix(a.cidr) : parsePrefix(a.address);
        if (p) out.push(p);
      }
    }
    return out;
  } catch {
    return null;
  }
}

// refusedAddress answers why the engine may not dial ip, or "" when it may.
// It refuses loopback, unspecified, private (RFC 1918), shared, benchmarking,
// link-local, multicast, reserved and unique local ranges; IPv6 forms that
// carry an IPv4 address (IPv4-mapped is checked as that address, NAT64 and
// 6to4 as the address inside, IPv4-compatible, IPv4-translated, local-use
// NAT64 and Teredo are refused whole); and any address on a network of
// `networks`, the host's own interfaces by default. When the networks cannot
// be read (null), every address is refused.
export function refusedAddress(ip: IP, networks: Prefix[] | null = hostNetworks()): string {
  const addr = unmap(ip);
  if (addr.length === 16) {
    if (inPrefix(NAT64, addr)) {
      const v4 = addr.slice(12);
      const why = refusedAddress(v4, networks);
      if (why) return `a NAT64 address of ${formatIP(v4)}, ${why}`;
    } else if (inPrefix(SIX_TO_FOUR, addr)) {
      const v4 = addr.slice(2, 6);
      const why = refusedAddress(v4, networks);
      if (why) return `a 6to4 address of ${formatIP(v4)}, ${why}`;
    }
  }
  for (const [p, why] of REFUSED_RANGES) if (inPrefix(p, addr)) return why;
  if (networks === null) return "unknown: this host's interfaces could not be read";
  for (const n of networks) {
    if (inPrefix(n, addr)) return `on ${formatIP(n.ip)}/${n.bits}, a network of this host's interfaces`;
  }
  return "";
}

// parseAllowLocal reads the operator's opt-in: addresses or CIDR prefixes,
// as a list or one comma-separated string. A malformed entry throws, so the
// engine refuses to boot rather than open more or less than was meant.
export function parseAllowLocal(value: string[] | string | undefined): Prefix[] {
  const items = Array.isArray(value) ? value : (value ?? "").split(",");
  const out: Prefix[] = [];
  for (const raw of items) {
    const s = raw.trim();
    if (!s) continue;
    const p = parsePrefix(s);
    if (!p) throw new Error(`allowLocal: ${JSON.stringify(s)} is not an IP address or CIDR prefix`);
    // An IPv4-mapped entry is stored as the IPv4 prefix it names.
    const ip = unmap(p.ip);
    out.push({ ip, bits: ip.length === p.ip.length ? p.bits : Math.max(0, p.bits - 96) });
  }
  return out;
}

// EgressRefused is a connection the engine would not open: status is the
// HTTP status the box is answered with, reason the text it is told.
export class EgressRefused extends Error {
  constructor(readonly status: number, reason: string, readonly address?: string) { super(reason); }
}

// REFUSED_HEADER carries the reason on a refusal, one line, so `curl -v`
// shows it for a CONNECT whose body curl does not print.
export const REFUSED_HEADER = "X-Dabs-Egress-Refused";

// refusalResponse is a complete HTTP response for a refusal.
export function refusalResponse(e: unknown): string {
  const r = e instanceof EgressRefused ? e : new EgressRefused(502, (e as Error)?.message ?? String(e));
  const reason = r.message.trim();
  const line = reason.replace(/[\x00-\x1f\x7f]/g, " ");
  const body = reason + "\n";
  const text = r.status === 403 ? "Forbidden" : r.status === 400 ? "Bad Request" : "Bad Gateway";
  return `HTTP/1.1 ${r.status} ${text}\r\nContent-Type: text/plain; charset=utf-8\r\n${REFUSED_HEADER}: ${line}\r\nContent-Length: ${Buffer.byteLength(body)}\r\nConnection: close\r\n\r\n${body}`;
}

// Resolve looks a name up and returns every address it has.
export type Resolve = (host: string) => Promise<string[]>;
const systemResolve: Resolve = async (host) =>
  (await dns.promises.lookup(host, { all: true, verbatim: true })).map((a) => a.address);

// Dial opens a connection for a box. proto names the proxy path, for the ledger.
type Dial = (host: string, port: number, proto: string) => Promise<net.Socket>;

// checkAddress answers why addr may not be dialled, or "" when it may: an
// address inside an allowLocal prefix may always be dialled.
function checkAddress(addr: string, allowLocal: Prefix[]): string {
  const ip = parseIP(addr);
  if (!ip) return "not an IP address";
  if (allowLocal.some((p) => inPrefix(p, unmap(ip)))) return "";
  return refusedAddress(ip);
}

// resolveChecked returns the addresses host may be dialled at, or throws an
// EgressRefused: 403 when any address is refused, 502 when there is none.
async function resolveChecked(host: string, allowLocal: Prefix[], resolve: Resolve): Promise<string[]> {
  const lit = literalAddress(host);
  let addrs: string[];
  if (lit) addrs = [lit];
  else {
    try { addrs = await resolve(host); }
    catch (e) { throw new EgressRefused(502, `looking up ${host}: ${(e as Error)?.message ?? e}`); }
  }
  if (!addrs.length) throw new EgressRefused(502, `${host} has no address`);
  for (const a of addrs) {
    const why = checkAddress(a, allowLocal);
    if (why) throw new EgressRefused(403, `${host} is ${a}, ${why}; egress never reaches this host, its networks or a private range`, a);
  }
  return addrs;
}

// connectTo connects to the first address that answers.
function connectTo(addrs: string[], port: number, host: string): Promise<net.Socket> {
  return new Promise((resolve, reject) => {
    const failures: string[] = [];
    const attempt = (i: number) => {
      if (i >= addrs.length) { reject(new EgressRefused(502, `connecting to ${host}: ${failures.join("; ")}`)); return; }
      const sock = net.connect({ host: addrs[i], port });
      const onError = (e: Error) => { failures.push(`${addrs[i]}: ${e.message}`); sock.destroy(); attempt(i + 1); };
      sock.once("error", onError);
      sock.once("connect", () => {
        sock.removeListener("error", onError);
        sock.on("error", () => {}); // the caller adds its own; this keeps an early error from throwing
        resolve(sock);
      });
    };
    attempt(0);
  });
}

// --- chain: load module hooks at boot; classify tls boundary directives. ----
async function resolveChain(chain: HopConfig[]): Promise<Hop[]> {
  const hops: Hop[] = [];
  for (const h of chain) {
    if (h.tls === "terminate" || h.tls === "originate") {
      hops.push({ name: "tls", kind: h.tls === "terminate" ? "tls-terminate" : "tls-originate", domains: h.domains, failOpen: h.failOpen });
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
// terminate…originate window — only those see decrypted HTTP content.
// connectHops is the subset OUTSIDE any window that exports onConnect — only
// those are asked about a dialed host:port. Report whether the chain terminates
// (needs a CA) and originates (forwards upstream), and the terminate window's
// domain scope.
function classify(hops: Hop[]) {
  const contentHops: Hop[] = [];
  const connectHops: Hop[] = [];
  let inWindow = false, terminates = false, originates = false;
  let terminateDomains: string[] | null = null;
  let failOpen = false;
  for (const h of hops) {
    if (h.kind === "tls-terminate") {
      inWindow = true;
      terminates = true;
      // An empty/absent list means "terminate every host"; a list scopes it.
      terminateDomains = h.domains && h.domains.length ? h.domains.map((d) => d.toLowerCase()) : null;
      // fail_open on the terminate hop: a host in scope we cannot intercept is
      // tunnelled instead of refused (default false = fail closed).
      failOpen = !!h.failOpen;
      continue;
    }
    if (h.kind === "tls-originate") { inWindow = false; originates = true; continue; }
    if (inWindow) { contentHops.push(h); continue; }
    if (h.handler?.onConnect) connectHops.push(h);
    // The warning is about the CONTENT verbs, so it is owed to any hop outside a
    // window that exports one — including one whose onConnect does run. Half a
    // working handler is the case most likely to be mistaken for a whole one.
    if (h.handler?.onRequest || h.handler?.onResponse || h.handler?.onRequestChunk || h.handler?.onResponseChunk) {
      console.warn(`warning: proxy hook "${h.name}" is OUTSIDE a "tls: terminate" window — its content verbs will NOT run. Add "tls: terminate" before it to inspect content.`);
    }
  }
  return { contentHops, connectHops, terminates, originates, terminateDomains, failOpen };
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

// --- CONNECTION tier: the module verdict on one dialed destination. ----------

// A refused connection names the hop that refused it and why, for the ledger.
interface ConnectDecision { allowed: boolean; hop?: string; reason?: string; }

// A reason is hook-supplied text landing in a host-side file, one line per
// refused connection, and a box can retry as often as it likes. Cap it: a hook
// must not be able to choose how many bytes a denial costs the host.
const MAX_REASON_CHARS = 200;
function clampReason(s: string): string {
  return s.length <= MAX_REASON_CHARS ? s : s.slice(0, MAX_REASON_CHARS) + "…";
}
function refuse(hop: string, reason: string): ConnectDecision {
  return { allowed: false, hop, reason: clampReason(reason) };
}

// askConnectHops walks the outside-the-window module hops in chain order
// (box→internet) and returns the first DENY, or an allow if every hop passed.
// The verdict is narrowing only: the caller has already applied the static
// policy, and no verdict here can turn a static deny into a reach.
//
// The tier fails closed. A hook that throws, exceeds the callHook bound, or
// answers with anything but a well-formed verdict DENIES the connection — a
// broken hook must not become an open door, and callHook's rejection is what
// keeps a hung hook from stalling the dial forever.
async function askConnectHops(connectHops: Hop[], host: string, port: number): Promise<ConnectDecision> {
  for (const hop of connectHops) {
    const fn = hop.handler?.onConnect;
    if (!fn) continue;
    let verdict: Json | void;
    try { verdict = await callHook(fn.call(hop.handler, { host, port }, {})); }
    catch (e) { return refuse(hop.name, `onConnect failed: ${(e as Error)?.message ?? e}`); }
    if (verdict == null) continue;
    if (typeof verdict !== "object") return refuse(hop.name, `onConnect returned ${typeof verdict}, not a verdict`);
    if (verdict.action === "allow") continue;
    if (verdict.action === "deny") return refuse(hop.name, String(verdict.reason ?? "denied"));
    return refuse(hop.name, `onConnect returned an unknown action: ${JSON.stringify(verdict.action)}`);
  }
  return { allowed: true };
}

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
  dial: Dial,
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
  // An IPv6 literal goes back into brackets in the Host header.
  const hostLiteral = host.includes(":") ? `[${host}]` : host;
  const authority = isDefaultPort ? hostLiteral : `${hostLiteral}:${port}`;
  const outHeaders: Record<string, string> = {};
  for (const [k, v] of Object.entries(reqHead.headers)) {
    const lk = k.toLowerCase();
    if (lk === "host" || lk === "proxy-connection" || lk === "content-length" || lk === "connection") continue;
    outHeaders[k] = v;
  }
  outHeaders["host"] = authority;
  outHeaders["connection"] = "close";

  // The connection is opened by dial, which checks the address; the HTTP
  // client gets that socket and never looks the name up itself. TLS to the
  // origin runs over it and checks the certificate against the name.
  let sock: net.Socket;
  try { sock = await dial(host, port, scheme === "https" ? "connect" : "http"); }
  catch (e) {
    await drain(bodyChunks(reader, reqHead.headers, method));
    client.end(refusalResponse(e));
    return;
  }
  const mod = scheme === "https" ? https : http;
  const createConnection = () => (scheme === "https" ? tls.connect({ socket: sock, host, servername: net.isIP(host) ? undefined : host, rejectUnauthorized: true }) : sock);
  const upReq = mod.request({ host, port, method, path, headers: outHeaders, createConnection } as http.RequestOptions);

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
    const msg = (e as Error)?.message ?? String(e);
    logEvent({ t: "stream-error", stage: "request", host, path, msg });
    console.error(`warning: request stream failed for ${host}: ${msg}`);
    return;
  }

  let upRes: http.IncomingMessage;
  try { upRes = await upResPromise; }
  catch (e) {
    client.end("HTTP/1.1 502 Bad Gateway\r\n\r\n");
    const msg = (e as Error)?.message ?? String(e);
    logEvent({ t: "stream-error", stage: "upstream", host, path, msg });
    console.error(`warning: upstream error for ${host}: ${msg}`);
    return;
  }
  logEvent({ t: "upstream", host, path, status: upRes.statusCode ?? 0 });

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
    const msg = (e as Error)?.message ?? String(e);
    logEvent({ t: "stream-error", stage: "response", host, msg });
    console.error(`warning: response stream failed for ${host}: ${msg}`);
    client.destroy();
  }
}

// --- boot -------------------------------------------------------------------
export async function start(cfg: EngineConfig): Promise<{ stop: () => void }> {
  const hops = await resolveChain(cfg.chain);
  const { contentHops, connectHops, terminates, originates, terminateDomains, failOpen } = classify(hops);
  const policyAllows = compilePolicy(cfg.allow ?? [], cfg.deny ?? []);

  // shouldInspect decides whether THIS host's TLS is terminated and its content
  // inspected. With no `domains` list a terminate window covers every host; a
  // list scopes it with the SAME pattern grammar as the gate (matchHost), so
  // `*.example.com`, a bare apex, or an exact host all mean here what they mean
  // there. Traffic we don't care about passes through un-decrypted.
  function shouldInspect(host: string): boolean {
    if (!terminates) return false;
    if (!terminateDomains) return true;
    return terminateDomains.some((d) => matchHost(d, host));
  }
  // Connection ledger: when DABS_CONNECT_LOG is set (inherited from the host dabs
  // process — `DABS_CONNECT_LOG=~/.dabs/broker/connect.jsonl dabs recipe …`), append
  // one JSON line per dialed destination with its disposition. This is the ONLY
  // record of a connection the content hooks never see — a raw tunnel, an ECH
  // drop, or a policy DENY — so it is how you prove you caught every edge (and
  // whether a box's traffic is proxied at all, or being denied).
  const connLogPath = process.env.DABS_CONNECT_LOG
    ? process.env.DABS_CONNECT_LOG.replace(/^~\//, (process.env.HOME ?? "") + "/")
    : "";
  logEvent = (o: object) => {
    if (!connLogPath) return;
    try { appendFileSync(connLogPath, JSON.stringify(o) + "\n"); } catch {}
  };
  const connLog = (host: string, port: number, verdict: string, proto = "connect", extra?: Json) =>
    logEvent({ t: "connect", proto, host, port, verdict, ...(extra ?? {}) });

  // gateConnection is the whole gate for one dialed destination, both proxy
  // paths: the engine-native static policy, then the module verdict. A refusal
  // is recorded in the ledger and nowhere else — the same treatment a static
  // deny gets, and the ledger keeps the two apart ("deny" is the recipe's own
  // allow/deny, "deny-module" a hook's onConnect, with the hop and its reason).
  // The address check comes later, in dial, when a connection is opened.
  async function gateConnection(host: string, port: number, proto: string): Promise<boolean> {
    if (!policyAllows(host)) { connLog(host, port, "deny", proto); return false; }
    const decision = await askConnectHops(connectHops, host, port);
    if (decision.allowed) return true;
    connLog(host, port, "deny-module", proto, { hop: decision.hop, reason: decision.reason });
    return false;
  }

  // The operator's opt-in to local addresses; a malformed one stops the boot.
  const allowLocal = parseAllowLocal(cfg.allowLocal ?? process.env.DABS_EGRESS_ALLOW_LOCAL);
  if (allowLocal.length) console.warn(`warning: egress may reach these local or private addresses: ${allowLocal.map((p) => `${formatIP(p.ip)}/${p.bits}`).join(", ")}`);
  const resolve = cfg.resolve ?? systemResolve;

  // checkTarget resolves host and checks every address; connectTarget opens
  // a connection to addresses checkTarget returned; dial is the two in a row, and
  // opens every connection the engine makes for a box. A refusal or a failure
  // is recorded in the ledger and thrown as an EgressRefused for the caller to
  // answer the box with.
  const failed = (e: unknown, host: string, port: number, proto: string): EgressRefused => {
    const r = e instanceof EgressRefused ? e : new EgressRefused(502, (e as Error)?.message ?? String(e));
    connLog(host, port, r.status === 403 ? "deny-address" : "unreachable", proto, { ...(r.address ? { address: r.address } : {}), reason: clampReason(r.message) });
    return r;
  };
  const checkTarget = async (host: string, port: number, proto: string): Promise<string[]> => {
    try { return await resolveChecked(host, allowLocal, resolve); }
    catch (e) { throw failed(e, host, port, proto); }
  };
  const connectTarget = async (addrs: string[], host: string, port: number, proto: string): Promise<net.Socket> => {
    try { return await connectTo(addrs, port, host); }
    catch (e) { throw failed(e, host, port, proto); }
  };
  const dial: Dial = async (host, port, proto) => connectTarget(await checkTarget(host, port, proto), host, port, proto);

  // Always mint the CA: dabs mounts ca.crt into every proxy box (the mount origin
  // must exist), and a chain that gains a `tls: terminate` later needs it anyway.
  ensureCA(cfg.caDir);

  const pinnedHosts = new Set<string>(); // clients that rejected our leaf (pinning)

  // fail_open (from the `tls: terminate` hop): a host we CANNOT intercept (it pins
  // its cert) or cannot stand a terminator up for is TUNNELED raw and recorded,
  // instead of refused. For an OBSERVE-only proxy, which must keep the box alive
  // and note what it could not see — never for a credential-injecting one, where
  // failing closed is the guarantee. Off by default (fail closed).

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
    // A unix socket path caps at ~104 bytes (macOS) and the temp caDir is already
    // long, so the NAME must stay short: a descriptive `term-api.anthropic.com_443`
    // overflows it and the listen fails. A short hash of host:port is unique enough.
    const sock = `${cfg.caDir}/t${Bun.hash(`${host}:${port}`).toString(36).slice(0, 12)}.sock`;
    if (existsSync(sock)) { try { unlinkSync(sock); } catch {} }
    const srv = tls.createServer({ cert: leaf.cert, key: leaf.key, ALPNProtocols: ["http/1.1"] }, (tlsSock) => {
      onDecrypted(tlsSock, host, port).catch((e) => { console.error(`warning: proxy content error for ${host}: ${e?.message ?? e}`); tlsSock.destroy(); });
    });
    // A client that PINS its cert rejects our leaf → the handshake fails here.
    srv.on("tlsClientError", () => {
      if (failOpen) pinnedHosts.add(host); // next connection to this host tunnels raw
      else console.warn(`warning: refused ${host}: cannot intercept (client pins its cert) and inspection is required — failing closed`);
    });
    // Bind the per-host terminator socket. A listen failure must REJECT (not throw
    // an unhandled 'error' that takes the whole engine down) so the CONNECT path
    // can fall back — one host's terminator failing is not the engine's death.
    await new Promise<void>((resolve, reject) => {
      srv.once("error", reject);
      srv.listen(sock, () => { srv.removeListener("error", reject); resolve(); });
    });
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
      await serveHTTP(tlsSock as unknown as net.Socket, contentHops, originates, host, port, "https", first, dial);
      return;
    }
    // The box's stream is already past its TLS handshake, so a refusal can
    // only close it; dial has recorded why in the ledger.
    let raw: net.Socket;
    try { raw = await dial(host, port, "connect"); }
    catch { tlsSock.destroy(); return; }
    const up = tls.connect({ socket: raw, host, servername: net.isIP(host) ? undefined : host, rejectUnauthorized: true }, () => {
      up.write(first);
      up.pipe(tlsSock);
      tlsSock.pipe(up);
    });
    const done = () => { tlsSock.destroy(); up.destroy(); };
    tlsSock.on("close", done); up.on("close", done); up.on("error", done);
  }

  // tunnel carries an allowed connection's raw bytes to an open upstream
  // socket, replaying the first chunk the box sent.
  function tunnel(client: net.Socket, up: net.Socket, first: Buffer) {
    if (client.destroyed) { up.destroy(); return; }
    up.on("error", () => client.destroy());
    client.on("close", () => up.destroy());
    up.write(first);
    client.pipe(up);
    up.pipe(client);
  }

  // answer ends a client the engine will not serve. The client is resumed so
  // whatever it still sends is read and dropped, and the close completes.
  function answer(client: net.Socket, response: string) {
    client.resume();
    client.end(response);
  }

  if (existsSync(cfg.socket)) { try { unlinkSync(cfg.socket); } catch {} }
  const server = net.createServer((client) => {
    client.on("error", () => {});
    client.once("data", async (buf) => {
      // The client is PAUSED from here until the next stage attaches its
      // reader. Between this listener firing and that reader, the socket would
      // otherwise flow with nothing listening, across real awaits (an async
      // onConnect hook, a name lookup, a dial), and bytes emitted then would be
      // dropped. Pausing holds them in the socket's own buffer; each resume
      // lands in the same tick as the reader that takes them.
      client.pause();
      try {
        const line = buf.toString("latin1").split("\r\n")[0];
        // The authority is host:port, or [v6]:port for an IPv6 literal.
        const connect = line.match(/^CONNECT\s+(\[[^\]\s]+\]|[^\s:[\]]+):(\d+)(?:\s|$)/i);
        if (connect) {
          const host = canonicalHost(connect[1]);
          const port = Number(connect[2]);
          // POLICY then CONNECTION: the engine-native CONNECT gate, on the
          // plaintext host, before any tunnel — for every protocol — and then
          // the module hops outside a tls window. A refused host never gets a
          // tunnel.
          if (!(await gateConnection(host, port, "connect"))) { answer(client, "HTTP/1.1 403 Forbidden\r\n\r\n"); return; }

          // A connection that will be tunneled is looked up and checked before
          // the 200, so a refused address is answered with a status the box can
          // read, and connected to after the box's first bytes. An inspected
          // one is checked later, when the engine dials it.
          const passthrough = failOpen && pinnedHosts.has(host);
          const inspect = shouldInspect(host) && !passthrough;
          let addrs: string[] = [];
          if (!inspect) {
            try { addrs = await checkTarget(host, port, "connect"); }
            catch (e) { answer(client, refusalResponse(e)); return; }
          }
          client.resume();
          client.write("HTTP/1.1 200 Connection Established\r\n\r\n");

          const hello = await firstChunk(client);
          if (!hello) { connLog(host, port, "no-hello"); client.destroy(); return; }
          if (!ALLOW_ECH && helloHasECH(hello)) { connLog(host, port, "ech-drop"); client.destroy(); return; } // ECH hides the SNI

          if (inspect) {
            let termSock: string;
            try {
              termSock = await terminatorFor(host, port);
            } catch (e) {
              // The terminator could not be stood up (e.g. a socket bind failure).
              // Best-effort tunnels raw and records it; strict inspection refuses
              // cleanly. Either way the engine stays up.
              if (failOpen) {
                connLog(host, port, "tunnel-noterminator");
                client.pause();
                let raw: net.Socket;
                try { raw = await dial(host, port, "connect"); }
                catch { client.destroy(); return; }
                client.resume();
                tunnel(client, raw, hello);
                return;
              }
              connLog(host, port, "refused-noterminator");
              console.warn(`warning: refused ${host}: no terminator (${(e as Error)?.message ?? e}) and inspection is required — failing closed`);
              client.destroy();
              return;
            }
            connLog(host, port, "inspect");
            // Pipe the client's raw TLS into the host's terminator, replaying the
            // buffered ClientHello. The terminator decrypts and calls onDecrypted.
            const inner = net.connect(termSock, () => {
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
          connLog(host, port, passthrough ? "tunnel-pinned" : "tunnel");
          client.pause();
          let up: net.Socket;
          try { up = await connectTarget(addrs, host, port, "connect"); }
          catch { client.destroy(); return; }
          client.resume();
          tunnel(client, up, hello);
          return;
        }
        const httpReq = line.match(/^([A-Z]+)\s+(http:\/\/\S+)\s+HTTP\/1\.[01]/i);
        if (httpReq) {
          // A plain forward-proxy request (`GET http://host/path`) — already
          // plaintext. Gate the host, then serve it through the content hops.
          const url = new URL(httpReq[2]);
          const host = canonicalHost(url.hostname);
          const port = Number(url.port || 80);
          if (!(await gateConnection(host, port, "http"))) { answer(client, "HTTP/1.1 403 Forbidden\r\n\r\n"); return; }
          connLog(host, port, shouldInspect(host) ? "inspect" : "forward", "http");
          const hooks = shouldInspect(host) ? contentHops : [];
          client.resume();
          await serveHTTP(client, hooks, hooks.length ? originates : true, host, port, "http", buf, dial);
          return;
        }
        connLog(line.split(/\s+/)[0] || "?", 0, "unhandled");
        answer(client, "HTTP/1.1 501 Not Implemented\r\n\r\n");
      } catch (e) {
        console.error(`warning: proxy connection error: ${(e as Error)?.message ?? e}`);
        try { answer(client, "HTTP/1.1 403 Forbidden\r\n\r\n"); } catch {}
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
