//go:build e2e

// Egress end-to-end: a box whose ONLY egress is a dabs proxy engine enforcing a
// recipe's policy (allow/deny at CONNECT) and, optionally, an HTTP content chain
// (http_proxy) of streaming four-verb hooks. The box hits https://<host>/<path>
// and the engine — not the real internet — decides and answers. Assertions land
// on BOTH sides: what the in-box process received, and what each hook saw.
//
// The engine never buffers a body: request and response bodies stream through
// the hooks chunk by chunk. Content hooks are pure per-request transforms:
//
//	onRequest(head)        edit request headers, or respond/deny
//	onRequestChunk(chunk)  transform each request body chunk (null = EOF)
//	onResponse(head)       edit status/headers
//	onResponseChunk(chunk) transform each response body chunk (null = EOF)
package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The inner hook: nearest the box. Logs every request path, answers /secret
// itself, and streams a "inner-saw:" prefix onto the body of any response it
// forwarded (proving a hook can both stop a request and transform a response,
// without buffering).
const innerHook = `import { appendFileSync } from "node:fs";
export default (config) => ({
  onRequest(head) {
    appendFileSync(config.log, head.path + "\n");
    if (head.path === "/secret") return { action: "respond", status: 200, body: "inner-secret" };
  },
  onResponseChunk(chunk, ctx) {
    if (chunk === null) return;
    if (!ctx.pre) { ctx.pre = true; return Buffer.concat([Buffer.from("inner-saw:"), chunk]); }
  },
});
`

// The outer hook: nearest the internet. Logs what it sees and answers terminally.
// It must NEVER see /secret — the inner hook stops that one.
const outerHook = `import { appendFileSync } from "node:fs";
export default (config) => ({
  onRequest(head) {
    appendFileSync(config.log, head.path + "\n");
    return { action: "respond", status: 200, body: "outer:" + head.path };
  },
});
`

// The in-box process: curl each endpoint through the proxy and record
// "<name> <status> <body>" so the host side can assert what the box actually saw.
const boxScript = `#!/bin/sh
out=/out/out.txt
: > "$out"
for p in secret public; do
  resp=$(curl -s -w '\n%{http_code}' "https://dabs.dev/$p")
  body=$(echo "$resp" | head -n1)
  code=$(echo "$resp" | tail -n1)
  echo "$p $code $body" >> "$out"
done
`

func TestProxyChainStopsAndModifies(t *testing.T) {
	clean(t)

	dir, err := os.MkdirTemp(home, "proxytest-")
	if err != nil {
		t.Fatal(err)
	}
	innerLog := filepath.Join(dir, "inner.log")
	outerLog := filepath.Join(dir, "outer.log")
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("inner.ts", innerHook, 0o644)
	write("outer.ts", outerHook, 0o644)
	if err := os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(boxScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// The recipe: reuse the staged `dabs-e2e` image (has curl), and make the two
	// hooks the box's ONLY way out. The window is closed by `tls: originate`, but
	// the outer hook answers first, so nothing forwards to the real internet.
	yaml := fmt.Sprintf(`default: proxytest
recipes:
  proxytest:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - { module: %s/inner.ts, log: %s }
        - { module: %s/outer.ts, log: %s }
        - tls: originate
    sources:
      - mount: %s
        path: /out
`, dir, innerLog, dir, outerLog, outDir)
	write("dabs.yaml", yaml, 0o644)

	// Boot detached (starts the engine), then run the script via exec — exec runs
	// under the same proxy env + forwarder, and skips the recipe confirm prompt.
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)

	if out, code := run("dabs exec " + inst + " -- sh /out/run.sh"); code != 0 {
		t.Fatalf("box script failed (%d):\n%s", code, out)
	}

	// --- box side: what the in-box process actually received ---
	boxOut := readFile(t, filepath.Join(outDir, "out.txt"))
	// /secret: inner responds AND its own onResponseChunk streams onto that answer
	// — a hook that responds still post-processes its own answer.
	if !strings.Contains(boxOut, "secret 200 inner-saw:inner-secret") {
		t.Errorf("box did not get the inner hook's terminal answer for /secret (with its own onResponseChunk applied):\n%s", boxOut)
	}
	if !strings.Contains(boxOut, "public 200 inner-saw:outer:/public") {
		t.Errorf("box did not get the outer answer modified by the inner hook for /public:\n%s", boxOut)
	}

	// --- proxy side: visibility. The outer hook must NOT have seen /secret. ---
	outerSeen := readFile(t, outerLog)
	innerSeen := readFile(t, innerLog)
	if strings.Contains(outerSeen, "/secret") {
		t.Errorf("outer hook saw /secret — the inner hook should have stopped it:\n%s", outerSeen)
	}
	if !strings.Contains(outerSeen, "/public") {
		t.Errorf("outer hook should have seen /public:\n%s", outerSeen)
	}
	if !strings.Contains(innerSeen, "/secret") || !strings.Contains(innerSeen, "/public") {
		t.Errorf("inner hook should have seen both paths:\n%s", innerSeen)
	}
}

// TestProxyReapSparesUnrelatedBun proves the engine reap is SURGICAL: removing a
// proxy box kills that box's engine (a bun process) and nothing else. A bun
// process with no relation to dabs, running alongside, must survive — the reap
// targets one recorded PID, never "all bun".
func TestProxyReapSparesUnrelatedBun(t *testing.T) {
	clean(t)

	// A bun process that has nothing to do with dabs. It must outlive the reap.
	unrelated := exec.Command("bun", "-e", "setInterval(() => {}, 1000000)")
	if err := unrelated.Start(); err != nil {
		t.Fatalf("start unrelated bun: %v", err)
	}
	defer func() { _ = unrelated.Process.Kill() }()
	unrelatedPID := unrelated.Process.Pid

	// A proxy box; its engine is a SEPARATE bun process, recorded on the node.
	dir, err := os.MkdirTemp(home, "proxyreap-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "h.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "ok" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`default: reapbox
recipes:
  reapbox:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - { module: %s/h.ts }
        - tls: originate
`, dir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	enginePID, proxyDir := engineNode(t, out)

	if enginePID == 0 {
		t.Fatal("box node recorded no engine PID")
	}
	if !running(unrelatedPID) {
		t.Fatal("unrelated bun is not running before the reap")
	}
	if !running(enginePID) {
		t.Fatalf("engine bun (pid %d) is not running after boot", enginePID)
	}

	if out, code := run("dabs rm " + inst + " --yes"); code != 0 {
		t.Fatalf("rm failed (%d):\n%s", code, out)
	}
	time.Sleep(500 * time.Millisecond)

	// The box's engine is reaped: no longer running (in this box's non-reaping
	// PID 1 it may briefly be a zombie, which `running` treats as not-running),
	// and its temp dir is gone.
	if running(enginePID) {
		t.Errorf("engine bun (pid %d) still running after rm — not reaped", enginePID)
	}
	if _, err := os.Stat(proxyDir); !os.IsNotExist(err) {
		t.Errorf("engine temp dir %s was not removed by the reap", proxyDir)
	}
	// The unrelated bun is untouched — the reap did not sweep all bun processes.
	if !running(unrelatedPID) {
		t.Errorf("unrelated bun (pid %d) was killed by the reap — reaping is not surgical", unrelatedPID)
	}
}

// engineNode reads the box node's recorded proxy engine PID and temp dir off the
// `id:` line of a `dabs recipe --detach` report.
func engineNode(t *testing.T, out string) (int, string) {
	t.Helper()
	var id string
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "id: "); ok {
			id = strings.TrimSpace(v)
		}
	}
	if id == "" {
		t.Fatalf("no id line in boot output:\n%s", out)
	}
	data := readFile(t, filepath.Join(home, ".dabs", "nodes", id, "dabs-node.json"))
	var n struct {
		ProxyPID int    `json:"proxyPid"`
		ProxyDir string `json:"proxyDir"`
	}
	if err := json.Unmarshal([]byte(data), &n); err != nil {
		t.Fatalf("parse node json: %v\n%s", err, data)
	}
	return n.ProxyPID, n.ProxyDir
}

// running reports whether pid is a live, non-zombie process (reads /proc state).
func running(pid int) bool {
	if pid <= 0 {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false // process is gone
	}
	// stat is "pid (comm) STATE …"; the state char follows the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z' // Z = zombie = killed but unreaped
}

// instanceLine pulls the driver instance name off a `dabs recipe --detach`
// report (its `instance:` line), which exec/rm resolve.
func instanceLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if inst, ok := strings.CutPrefix(strings.TrimSpace(line), "instance: "); ok {
			return strings.TrimSpace(inst)
		}
	}
	t.Fatalf("no instance line in boot output:\n%s", out)
	return ""
}

// TestEgressDenyList covers the engine-native CONNECT gate as a denylist: a
// `deny:` pattern refuses one host (403 at CONNECT → curl 000) while everything
// else tunnels to the real host. No module, no tls — pure engine policy.
func TestEgressDenyList(t *testing.T) {
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "proxypolicy-")
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`default: gate
recipes:
  gate:
    image: dabs-e2e
    keep: true
    egress:
      deny: blocked.test
    sources:
      - mount: %s
        path: /out
`, outDir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
out=/out/out.txt
: > "$out"
echo "blocked $(curl -s -o /dev/null -w '%{http_code}' -m 8 https://blocked.test/ || echo 000)" >> "$out"
echo "allowed $(curl -s -o /dev/null -w '%{http_code}' -m 12 https://example.com/ || echo 000)" >> "$out"
`
	if err := os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	if out, code := run("dabs exec " + inst + " -- sh /out/run.sh"); code != 0 {
		t.Fatalf("script failed (%d):\n%s", code, out)
	}
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	// Deny is hermetic: refused at CONNECT.
	if !strings.Contains(got, "blocked 000") {
		t.Errorf("blocked.test should be denied at CONNECT (000):\n%s", got)
	}
	// Everything else tunnels to the real host (needs network in the e2e box).
	if !strings.Contains(got, "allowed 200") {
		t.Errorf("example.com should be allowed and reach the real host (200):\n%s", got)
	}
}

// TestProxyDenyCarriesHeaders is the regression for the `deny`-drops-headers bug:
// a content-tier deny (a hook answering with action:"deny") must emit its custom
// response headers, like respond.
func TestProxyDenyCarriesHeaders(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxydeny-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deny.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "deny", status: 403, headers: { "x-policy": "blocked" }, body: "no" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`default: deny
recipes:
  deny:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - module: %s/deny.ts
        - tls: originate
    sources:
      - mount: %s
        path: /out
`, dir, outDir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "run.sh"),
		[]byte("#!/bin/sh\ncurl -s -D- -o /dev/null -m 8 https://x.test/ > /out/headers.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	if out, code := run("dabs exec " + inst + " -- sh /out/run.sh"); code != 0 {
		t.Fatalf("script failed (%d):\n%s", code, out)
	}
	hdrs := readFile(t, filepath.Join(outDir, "headers.txt"))
	if !strings.Contains(hdrs, "403") {
		t.Errorf("deny should return 403:\n%s", hdrs)
	}
	if !strings.Contains(strings.ToLower(hdrs), "x-policy") {
		t.Errorf("deny must carry its custom headers (x-policy) to the client:\n%s", hdrs)
	}
}

// TestProxyBadModulePathErrorsClearly is the regression for the opaque-boot bug:
// a wrong module path must error clearly at boot, not as a socket timeout.
func TestProxyBadModulePathErrorsClearly(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxybad-")
	if err != nil {
		t.Fatal(err)
	}
	yaml := `default: bad
recipes:
  bad:
    image: dabs-e2e
    egress:
      http_proxy:
        - tls: terminate
        - module: /nope/does-not-exist.ts
        - tls: originate
`
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("expected a boot failure for a missing module, got success:\n%s", out)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("want a clear not-found error naming the module, got:\n%s", out)
	}
	if strings.Contains(out, "socket") {
		t.Errorf("must not surface an opaque socket timeout:\n%s", out)
	}
}

// TestProxyInjectsEcosystemCAEnv checks the box gets the CA-trust and proxy env
// vars the common tools actually read — including GIT_SSL_CAINFO (git ignores
// CURL_CA_BUNDLE) and NODE_USE_ENV_PROXY (node's fetch/https ignore proxy env).
// A pure allow-all gate (no chain) still routes egress through the engine.
func TestProxyInjectsEcosystemCAEnv(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyenv-")
	if err != nil {
		t.Fatal(err)
	}
	yaml := "default: e\nrecipes:\n  e:\n    image: dabs-e2e\n    keep: true\n    egress:\n      allow: \"*\"\n"
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	env, _ := run("dabs exec " + inst + " -- env")
	for _, want := range []string{"GIT_SSL_CAINFO=/run/dabs/pub/ca.crt", "NODE_USE_ENV_PROXY=1", "CURL_CA_BUNDLE=/run/dabs/pub/ca.crt", "HTTPS_PROXY=http://127.0.0.1:18080"} {
		if !strings.Contains(env, want) {
			t.Errorf("box env missing %q:\n%s", want, env)
		}
	}
}

// TestEgressHostCanonicalizationHolds is the regression for the case/trailing-dot
// denylist bypass: the engine keys policy on the CANONICAL host, so a deny of a
// lowercase host also blocks its uppercase and trailing-dot spellings (which
// reach the same server). An unrelated allowed host still gets through, answered
// by a content hook.
func TestEgressHostCanonicalizationHolds(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxycanon-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "ok" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: c\nrecipes:\n  c:\n    image: dabs-e2e\n    keep: true\n    egress:\n      deny: block.test\n      http_proxy:\n        - tls: terminate\n        - module: %s/hook.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
: > "$out"
code() { curl -s -o /dev/null -w '%{http_code}' -m 8 "$1" || echo 000; }
echo "lower $(code https://block.test/)" >> "$out"
echo "upper $(code https://BLOCK.TEST/)" >> "$out"
echo "dot $(code https://block.test./)" >> "$out"
echo "allowed $(code https://allow.test/)" >> "$out"
`), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	for _, want := range []string{"lower 000", "upper 000", "dot 000", "allowed 200"} {
		if !strings.Contains(got, want) {
			t.Errorf("canonicalization: want %q in output:\n%s", want, got)
		}
	}
}

// TestProxyMalformedFactoryFailsClosed is the regression for a factory that
// returns a non-handler (null/number): it must fail the BOOT loudly, not become a
// silent pass-through that opens egress with no hooks running.
func TestProxyMalformedFactoryFailsClosed(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxynull-")
	if err != nil {
		t.Fatal(err)
	}
	// A valid default export that is a function — but it returns null, not a handler.
	if err := os.WriteFile(filepath.Join(dir, "nullish.ts"),
		[]byte(`export default () => null;`), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("default: n\nrecipes:\n  n:\n    image: dabs-e2e\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/nullish.ts\n        - tls: originate\n", dir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("expected a boot failure for a factory returning null, got success:\n%s", out)
	}
	if !strings.Contains(out, "handler") {
		t.Errorf("want an error naming the bad handler return, got:\n%s", out)
	}
}

// TestProxyFailedBootLeavesNoTempDir is the regression for the engine temp-dir
// leak: when a module EXISTS (passes the path check) but the engine cannot boot
// — a syntax-error module here — Provision must clean the /tmp/dabs-proxy-XXXX
// dir it created. No box node carries the dir on failure, so nothing else can
// reap it; a leak would accumulate one dir per failed boot.
func TestProxyFailedBootLeavesNoTempDir(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyleak-")
	if err != nil {
		t.Fatal(err)
	}
	// A module that imports fine as a path but throws at load: the engine starts,
	// never binds its socket, and Start returns an error.
	if err := os.WriteFile(filepath.Join(dir, "boom.ts"),
		[]byte("this is not valid typescript ((("), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("default: boom\nrecipes:\n  boom:\n    image: dabs-e2e\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/boom.ts\n        - tls: originate\n", dir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := filepath.Glob("/tmp/dabs-proxy-*")
	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("expected a boot failure for a broken engine module, got success:\n%s", out)
	}
	after, _ := filepath.Glob("/tmp/dabs-proxy-*")
	if len(after) > len(before) {
		t.Errorf("failed engine boot leaked a temp dir: before=%v after=%v", before, after)
	}
}

// TestProxyDoesNotFollowRedirects is the regression for a domain-policy bypass:
// the engine must NOT follow a 30x server-side on the forwarded leg. Policy gates
// only the CONNECT host, so following a redirect could bounce the box to a host
// the policy never saw. The box must receive the 3xx itself (and re-request,
// which re-runs policy). http://github.com issues a permanent 301; if the engine
// followed it, the box would see the 200 of the https target instead.
func TestProxyDoesNotFollowRedirects(t *testing.T) {
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyredir-")
	if err != nil {
		t.Fatal(err)
	}
	// A pass-through content hook (no transform) so the request forwards upstream.
	if err := os.WriteFile(filepath.Join(dir, "fwd.ts"),
		[]byte(`export default () => ({ onRequest() {} });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: r\nrecipes:\n  r:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/fwd.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	// No -L: the box must see the redirect status itself, not the followed page.
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -o /dev/null -w 'code=%{http_code}\\n' -m 12 http://github.com/ > /out/resp.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	resp := readFile(t, filepath.Join(outDir, "resp.txt"))
	if !strings.Contains(resp, "code=301") {
		t.Errorf("the engine must not follow the redirect (want the box to see 301), got:\n%s", resp)
	}
}

// TestProxyReapsEngineWhenBoxEntryFails is the regression for the engine leak on
// a boot that fails AFTER the engine started: the policy is valid and the engine
// boots, but the box's workdir does not exist so the entry (smoke check) fails.
// No box node is written to carry the engine's PID, so buildBox must reap it on
// the failed return — otherwise the engine and its temp dir leak forever.
func TestProxyReapsEngineWhenBoxEntryFails(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyentry-")
	if err != nil {
		t.Fatal(err)
	}
	// A valid engine policy, but a workdir that does not exist in the box → the
	// post-boot entry fails after Provision already started the engine.
	yaml := "default: g\nrecipes:\n  g:\n    image: dabs-e2e\n    workdir: /does/not/exist\n    egress:\n      allow: \"*\"\n"
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	before, _ := filepath.Glob("/tmp/dabs-proxy-*")
	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("expected a boot failure for a missing workdir, got success:\n%s", out)
	}
	after, _ := filepath.Glob("/tmp/dabs-proxy-*")
	if len(after) > len(before) {
		t.Errorf("a failed box entry leaked the proxy engine's temp dir: before=%v after=%v", before, after)
	}
}

// TestProxyCarriesNonStandardPort is the regression for the dropped upstream
// port: a box connecting to a host on a non-standard port through a terminate
// window must have that port on the request head the engine sees (and
// re-originates to), not be silently rewritten to 443. The hook echoes the port.
func TestProxyCarriesNonStandardPort(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyport-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest(head) { return { action: "respond", status: 200, body: "port=" + head.port }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: p\nrecipes:\n  p:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/hook.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -m 8 https://mock.test:8443/ > /out/resp.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	resp := readFile(t, filepath.Join(outDir, "resp.txt"))
	if !strings.Contains(resp, "port=8443") {
		t.Errorf("the non-standard port must reach the engine's request head, got:\n%s", resp)
	}
}

// TestProxyOutsideWindowWarns is the regression for the invisible-warning bug: a
// content hook placed outside a tls window boots but prints a warning to dabs's
// stderr (relayed from the engine), not just the buried engine log.
func TestProxyOutsideWindowWarns(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxywarn-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "x" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A content hook (onRequest) with NO tls: terminate → it can never run.
	yaml := fmt.Sprintf(`default: warn
recipes:
  warn:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - module: %s/hook.ts
`, dir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("box should still boot (the warning is advisory), got failure:\n%s", out)
	}
	if !strings.Contains(out, "OUTSIDE") {
		t.Errorf("expected the boot warning about a hook outside a tls window on dabs stderr, got:\n%s", out)
	}
}

// TestProxyHookThrowSurvivesEngine is the regression for the DoS: a content hook
// that THROWS on one request must be contained (that request gets a 502), not
// crash the engine and kill all egress. We prove survival: a request that trips
// the throw fails, then a later request through the same engine still works.
func TestProxyHookThrowSurvivesEngine(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxythrow-")
	if err != nil {
		t.Fatal(err)
	}
	// Throw for one host; answer for any other. The engine isolates the throw
	// per connection, so the second request still gets its answer.
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest(head) { if (head.host === "boom.test") throw new Error("boom"); return { action: "respond", status: 200, body: "ok" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: g\nrecipes:\n  g:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/hook.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
: > "$out"
echo "throw $(curl -s -o /dev/null -w '%{http_code}' -m 8 https://boom.test/ || echo 000)" >> "$out"
echo "survived $(curl -s -m 12 https://ok.test/ || echo 000)" >> "$out"
`), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	if out, code := run("dabs exec " + inst + " -- sh /out/run.sh"); code != 0 {
		t.Fatalf("script failed (%d):\n%s", code, out)
	}
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	// The throwing request gets a 502; the point is the engine SURVIVES, so the
	// next request still gets the hook's answer.
	if !strings.Contains(got, "throw 502") {
		t.Errorf("a throwing hook should yield 502 for that request:\n%s", got)
	}
	if !strings.Contains(got, "survived ok") {
		t.Errorf("engine did not survive a hook throw (next request should be answered):\n%s", got)
	}
}

// TestProxyHookExceptionNoLeak is the regression for the info leak: a content
// hook that throws must yield a terse 502, never an error page carrying the
// engine source and host paths into the untrusted box.
func TestProxyHookExceptionNoLeak(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyleak-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "boom.ts"),
		[]byte(`export default () => ({ onRequest() { throw new Error("SECRET-HOOK-SOURCE-MARKER"); } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: b\nrecipes:\n  b:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/boom.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -m 8 https://x.test/ -w '\\ncode=%{http_code}\\n' > /out/resp.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	resp := readFile(t, filepath.Join(outDir, "resp.txt"))
	if !strings.Contains(resp, "code=502") {
		t.Errorf("a throwing hook should yield 502, got:\n%s", resp)
	}
	for _, leak := range []string{"engine.ts", "boom.ts", "SECRET-HOOK-SOURCE-MARKER", "/tmp/", ".ts:"} {
		if strings.Contains(resp, leak) {
			t.Errorf("response leaked engine internals (%q) into the box:\n%s", leak, resp)
		}
	}
}

// TestProxyOriginateForwards covers the HTTPS upstream-forwarding path (CONNECT →
// terminate → module → originate) that terminal chains never exercise: forward
// to a real host over a fresh upstream TLS and stream a prefix onto its real
// response on the way back. The originate leg re-encrypts to the upstream with
// certificate verification on, so it needs a host whose cert the engine trusts —
// hence a real host and the online subset. The hermetic side of the
// forward-and-transform path (plain HTTP, no upstream TLS) lives in
// TestProxyPlainHTTP against a loopback upstream.
func TestProxyOriginateForwards(t *testing.T) {
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyfwd-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mod.ts"),
		[]byte(`export default () => ({ onResponseChunk(chunk, ctx) { if (chunk === null) return; if (!ctx.p) { ctx.p = true; return Buffer.concat([Buffer.from("PROXIED:"), chunk]); } } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: f\nrecipes:\n  f:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/mod.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -m 15 https://example.com/ > /out/body.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	body := readFile(t, filepath.Join(outDir, "body.txt"))
	// The request really went to example.com and came back with the hook's prefix.
	if !strings.Contains(body, "PROXIED:") || !strings.Contains(body, "Example Domain") {
		t.Errorf("originate should forward to the real host and let the hook stream-transform the real response:\n%s", body)
	}
}

// TestProxyPlainHTTP covers plain-HTTP forward-proxy egress (GET http://…): the
// engine handles non-CONNECT requests, applies the content chain, and forwards
// to the upstream. The upstream is a loopback server this test process runs, so
// the coverage is hermetic — the engine originates plain HTTP (no upstream TLS)
// to 127.0.0.1. The box's proxy env exempts 127.0.0.1 via NO_PROXY, so the box
// curls with --noproxy ” to clear that exemption and route the request through
// the engine, which then originates to this process's loopback listener.
func TestProxyPlainHTTP(t *testing.T) {
	clean(t)

	const upstreamMark = "LOOPBACK-UPSTREAM-BODY"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, upstreamMark)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir, err := os.MkdirTemp(home, "proxyhttp-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mod.ts"),
		[]byte(`export default () => ({ onResponseChunk(chunk, ctx) { if (chunk === null) return; if (!ctx.p) { ctx.p = true; return Buffer.concat([Buffer.from("HTTP-PROXIED:"), chunk]); } } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: h\nrecipes:\n  h:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/mod.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	// http:// (not https) — the box emits a plain forward-proxy request. --noproxy
	// '' clears the NO_PROXY exemption for 127.0.0.1 so it routes through the engine.
	script := fmt.Sprintf("#!/bin/sh\ncurl -s --noproxy '' -m 15 http://127.0.0.1:%d/ -w '\\ncode=%%{http_code}\\n' > /out/resp.txt 2>&1\n", port)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(script), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	resp := readFile(t, filepath.Join(outDir, "resp.txt"))
	if !strings.Contains(resp, "HTTP-PROXIED:") || !strings.Contains(resp, upstreamMark) {
		t.Errorf("plain http:// should be proxied+forwarded to the loopback upstream; got:\n%s", resp)
	}
}

// TestProxyResponseHeaderInjectionFailsClosed is the regression for CRLF
// response-splitting: a hook response header value carrying "\r\n" must not
// inject headers/body into the box's response. The engine serializes the reply
// rejecting CRLF and fails closed with a 502 — the smuggled header must never
// reach the box.
func TestProxyResponseHeaderInjectionFailsClosed(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxycrlf-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evil.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "ok", headers: { "X-A": "a\r\nInjected: pwned" } }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: e\nrecipes:\n  e:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/evil.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -i -m 12 https://any.test/ > /out/resp.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	resp := readFile(t, filepath.Join(outDir, "resp.txt"))
	if strings.Contains(resp, "Injected") || strings.Contains(resp, "pwned") {
		t.Errorf("a CRLF header value must not split the response:\n%s", resp)
	}
}

// TestProxyBinaryBodyPreserved is the regression for binary corruption through a
// terminate window: the engine streams bodies as raw Buffers, so all 256 byte
// values survive. A hook responds with bytes 0x00..0xff (256 bytes); the box
// must receive exactly 256 bytes, not a UTF-8-inflated blob.
func TestProxyBinaryBodyPreserved(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxybin-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.ts"),
		[]byte(`export default () => ({ onRequest() { let b = ""; for (let i = 0; i < 256; i++) b += String.fromCharCode(i); return { action: "respond", status: 200, body: b }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: b\nrecipes:\n  b:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/bin.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -m 12 https://any.test/ | wc -c | tr -d ' \\n' > /out/size.txt\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	if got := strings.TrimSpace(readFile(t, filepath.Join(outDir, "size.txt"))); got != "256" {
		t.Errorf("binary body corrupted through terminate window: got %q bytes, want 256", got)
	}
}

// TestProxyDirectIPThroughWindow is the regression for the missing iPAddress SAN:
// a literal-IP host intercepted by a terminate window needs an IP SAN on the leaf
// or the client rejects the cert. curl to a real IP over the window must complete
// the TLS handshake (exit 0), not fail with a cert mismatch.
func TestProxyDirectIPThroughWindow(t *testing.T) {
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyip-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pass.ts"),
		[]byte(`export default () => ({ onRequest() {} });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: ip\nrecipes:\n  ip:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/pass.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	// 1.1.1.1 serves HTTPS; the status does not matter, only that the leaf's IP
	// SAN lets the handshake succeed (exit 0, not 60).
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -o /dev/null -m 15 https://1.1.1.1/; echo \"exit=$?\" > /out/ip.txt\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	got := readFile(t, filepath.Join(outDir, "ip.txt"))
	if !strings.Contains(got, "exit=0") {
		t.Errorf("direct-IP HTTPS through a terminate window should handshake (exit 0), got:\n%s", got)
	}
}

// TestProxyTerminateDomainsScopeInterception proves `tls: terminate` with a
// `domains` list decrypts ONLY the listed hosts: a listed host is intercepted by
// the content hook, while an unlisted host passes through un-decrypted to the
// real internet (the hook never sees it).
func TestProxyTerminateDomainsScopeInterception(t *testing.T) {
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "proxydomains-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "INTERCEPTED" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: d\nrecipes:\n  d:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n          domains: [mock.test]\n        - module: %s/hook.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
: > "$out"
echo "listed=$(curl -s -m 8 https://mock.test/)" >> "$out"
echo "unlisted=$(curl -s -m 12 https://example.com/ | grep -o 'Example Domain' | head -1)" >> "$out"
`), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	// The listed host is decrypted → the hook answers.
	if !strings.Contains(got, "listed=INTERCEPTED") {
		t.Errorf("a listed domain must be intercepted by the hook:\n%s", got)
	}
	// The unlisted host passes through un-decrypted to the real server.
	if !strings.Contains(got, "unlisted=Example Domain") {
		t.Errorf("an unlisted domain must pass through to the real host, not the hook:\n%s", got)
	}
}

// TestProxyBrokerHeaderRoundTrip is the flagship credential-broker path over
// HEADERS across a two-hop chain: the box sends a DUMMY Authorization token; the
// box-side broker injects the REAL token on the way out (onRequest header edit);
// the internet-side "API" hop echoes the header it actually received (proving the
// injection reached it); the broker scrubs the real token back to dummy on the
// way home (onResponseChunk). The box must end up seeing the dummy and NEVER the
// real secret.
func TestProxyBrokerHeaderRoundTrip(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxybroker-")
	if err != nil {
		t.Fatal(err)
	}
	// Box-side broker: inject real on request head, scrub back to dummy on the
	// streamed response.
	if err := os.WriteFile(filepath.Join(dir, "broker.ts"),
		[]byte(`export default () => ({ onRequest(head) { if (head.headers["authorization"] === "Bearer dummy") return { headers: { authorization: "Bearer REALSECRET" } }; }, onResponseChunk(chunk) { if (chunk === null) return; return Buffer.from(chunk.toString("latin1").split("REALSECRET").join("dummy")); } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Internet-side API: echo the Authorization header it received, terminally.
	if err := os.WriteFile(filepath.Join(dir, "api.ts"),
		[]byte(`export default () => ({ onRequest(head) { return { action: "respond", status: 200, body: "seen=" + (head.headers["authorization"] || "none") }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	// Chain box→internet: broker (nearest box) then api (nearest internet).
	yaml := fmt.Sprintf("default: br\nrecipes:\n  br:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/broker.ts\n        - module: %s/api.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -m 8 -H 'Authorization: Bearer dummy' https://api.test/ > /out/body.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	body := readFile(t, filepath.Join(outDir, "body.txt"))
	// The API hop saw the injected real token; the broker scrubbed it back.
	if !strings.Contains(body, "seen=Bearer dummy") {
		t.Errorf("broker swap-back failed — box should see the dummy token:\n%s", body)
	}
	if strings.Contains(body, "REALSECRET") {
		t.Errorf("the real secret leaked to the box:\n%s", body)
	}
}

// TestProxySingleHookSwapBack proves a single hook can both `respond` and
// post-process its own answer via onResponseChunk (the broker swap-back pattern):
// respond with REAL, then stream-swap REAL→DUMMY so the box sees only the safe
// value.
func TestProxySingleHookSwapBack(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyswap-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broker.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "REAL-SECRET" }; }, onResponseChunk(chunk) { if (chunk === null) return; return Buffer.from(chunk.toString("latin1").replace("REAL-SECRET", "DUMMY")); } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: b\nrecipes:\n  b:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/broker.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncurl -s -m 8 https://api.test/ > /out/body.txt 2>&1\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	body := readFile(t, filepath.Join(outDir, "body.txt"))
	if strings.Contains(body, "REAL-SECRET") {
		t.Errorf("single-hook onResponseChunk did not run on its own respond — real value leaked to the box:\n%s", body)
	}
	if !strings.Contains(body, "DUMMY") {
		t.Errorf("box should see the swapped-back DUMMY value:\n%s", body)
	}
}

// TestProxyHungHookTimesOut proves a hook that hangs is bounded, not an
// indefinite egress stall — the request returns quickly with a 502, well within
// curl's own timeout.
func TestProxyHungHookTimesOut(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyhang-")
	if err != nil {
		t.Fatal(err)
	}
	// onRequest hangs forever → bounded by the hook timeout → 502.
	if err := os.WriteFile(filepath.Join(dir, "hang.ts"),
		[]byte(`export default () => ({ async onRequest() { await new Promise(() => {}); } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: g\nrecipes:\n  g:\n    image: dabs-e2e\n    keep: true\n    egress:\n      http_proxy:\n        - tls: terminate\n        - module: %s/hang.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	// curl -m 8: if the hook hung unbounded, curl would hit its own timeout (28);
	// with the engine hook timeout it gets a quick 502.
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte("#!/bin/sh\ncode=$(curl -s -o /dev/null -w '%{http_code}' -m 8 https://x.test/); echo \"code=$code exit=$?\" > /out/r.txt\n"), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	r := readFile(t, filepath.Join(outDir, "r.txt"))
	if strings.Contains(r, "exit=28") {
		t.Errorf("a hung hook stalled egress past curl's timeout (no engine timeout):\n%s", r)
	}
	if !strings.Contains(r, "code=502") {
		t.Errorf("a hung hook should be bounded to a quick 502:\n%s", r)
	}
}

// TestProxyPinnedCertFailsClosed covers a client that PINS its cert (rejects our
// MITM leaf) on a `tls: terminate` chain: a terminate window declares inspection
// is REQUIRED, so a host we cannot intercept is refused, not waved through —
// passing it through would let a credential the host was meant to swap out reach
// the box. The pinned host stays unreachable; a trusting client is still
// intercepted; the CONNECT domain denylist STILL holds.
func TestProxyPinnedCertFailsClosed(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxypin-")
	if err != nil {
		t.Fatal(err)
	}
	// Content tier: a hook that would intercept. Policy: deny one domain via the
	// engine-native gate.
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "INTERCEPTED" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: p\nrecipes:\n  p:\n    image: dabs-e2e\n    keep: true\n    egress:\n      deny: blocked.test\n      http_proxy:\n        - tls: terminate\n        - module: %s/hook.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	// SYS is the box's system CA bundle — it does NOT include our MITM CA, so
	// `--cacert $SYS` makes curl reject our leaf (a stand-in for cert pinning).
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
SYS=/etc/ssl/certs/ca-certificates.crt
: > "$out"
# default (trusts our CA): the content hook intercepts.
echo "intercepted=$(curl -s -m 8 https://madeup.test/)" >> "$out"
# pinned (system CA): both requests reject our leaf. Failing closed, the engine
# does NOT learn a passthrough, so neither ever reaches the real example.com.
curl -s --cacert $SYS -o /dev/null -m 10 https://example.com/; echo "pin1_exit=$?" >> "$out"
echo "pin2=$(curl -s --cacert $SYS -m 12 https://example.com/ | grep -o 'Example Domain' | head -1)" >> "$out"
# the domain denylist still holds, even with tls: terminate in the chain.
curl -s --cacert $SYS -o /dev/null -m 8 https://blocked.test/; echo "deny_exit=$?" >> "$out"
`), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	if !strings.Contains(got, "intercepted=INTERCEPTED") {
		t.Errorf("a trusting client should be intercepted by the content hook:\n%s", got)
	}
	if strings.Contains(got, "pin2=Example Domain") {
		t.Errorf("a pinned client on a terminate chain must be refused, not reach the real host:\n%s", got)
	}
	if !strings.Contains(got, "pin1_exit=60") {
		t.Errorf("a pinned client should fail the TLS handshake (curl exit 60), not connect:\n%s", got)
	}
	if !strings.Contains(got, "deny_exit=56") {
		t.Errorf("the domain denylist must still block, even in a terminate chain:\n%s", got)
	}
}

// TestEgressAllowGatesTerminateChain proves the allow list holds even when a
// terminate window is present: an allowlisted host reaches the content hook while
// any other host is default-denied at CONNECT (HTTP does not get to skip the
// policy just because a content chain exists).
func TestEgressAllowGatesTerminateChain(t *testing.T) {
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyallowgate-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.ts"),
		[]byte(`export default () => ({ onRequest() { return { action: "respond", status: 200, body: "ok" }; } });`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	yaml := fmt.Sprintf("default: a\nrecipes:\n  a:\n    image: dabs-e2e\n    keep: true\n    egress:\n      allow: ok.test\n      http_proxy:\n        - tls: terminate\n        - module: %s/hook.ts\n        - tls: originate\n    sources:\n      - mount: %s\n        path: /out\n", dir, outDir)
	os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
: > "$out"
echo "allowed=$(curl -s -m 8 https://ok.test/)" >> "$out"
curl -s -o /dev/null -m 8 https://other.test/; echo "deny_exit=$?" >> "$out"
`), 0o755)
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	if !strings.Contains(got, "allowed=ok") {
		t.Errorf("an allowlisted host should reach the content hook (ok):\n%s", got)
	}
	if !strings.Contains(got, "deny_exit=56") {
		t.Errorf("an unlisted host must be default-denied at CONNECT (exit 56), even with a terminate chain:\n%s", got)
	}
}

// TestEgressAllowlistDefaultDeny is the allowlist shape (the inverse of the
// denylist): allow ONLY a named host and default-deny everything else. The listed
// host (example.com) reaches the real internet; the UNLISTED host is a LIVE
// endpoint, so its 000 proves a real CONNECT refusal, not an unreachable host.
func TestEgressAllowlistDefaultDeny(t *testing.T) {
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "proxyallow-")
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("default: allow\nrecipes:\n  allow:\n    image: dabs-e2e\n    keep: true\n    egress:\n      allow: example.com\n    sources:\n      - mount: %s\n        path: /out\n", outDir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
: > "$out"
echo "listed $(curl -s -o /dev/null -w '%{http_code}' -m 12 https://example.com/ || echo 000)" >> "$out"
echo "unlisted $(curl -s -o /dev/null -w '%{http_code}' -m 12 https://www.dabs.dev/test/hello || echo 000)" >> "$out"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	if out, code := run("dabs exec " + inst + " -- sh /out/run.sh"); code != 0 {
		t.Fatalf("script failed (%d):\n%s", code, out)
	}
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	// The one allowed host tunnels through to the live endpoint (content unchecked).
	if !strings.Contains(got, "listed 200") {
		t.Errorf("the allowlisted host should reach the real endpoint (200):\n%s", got)
	}
	// Everything not on the list is refused at CONNECT.
	if !strings.Contains(got, "unlisted 000") {
		t.Errorf("an unlisted host should be default-denied at CONNECT (000):\n%s", got)
	}
}

// TestEgressNoneCutsNetwork proves the closed-egress mode is a real wall, not
// just a parsed field: a box with `egress: none` has its network cut by the
// driver, so even a live host is unreachable.
func TestEgressNoneCutsNetwork(t *testing.T) {
	// Online subset. Meaningful only against a box that otherwise has network: in
	// the hermetic box egress is already cut, so a 000 would not isolate the
	// driver's cut.
	online(t)
	clean(t)
	dir, err := os.MkdirTemp(home, "egressnone-")
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("default: shut\nrecipes:\n  shut:\n    image: dabs-e2e\n    keep: true\n    egress: none\n    sources:\n      - mount: %s\n        path: /out\n", outDir)
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(`#!/bin/sh
out=/out/out.txt
: > "$out"
echo "code $(curl -s -o /dev/null -w '%{http_code}' -m 8 https://www.dabs.dev/test/hello || echo 000)" >> "$out"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)
	run("dabs exec " + inst + " -- sh /out/run.sh")
	got := readFile(t, filepath.Join(outDir, "out.txt"))
	// No network → curl cannot connect → our sentinel 000, never a live 200.
	if !strings.Contains(got, "code 000") {
		t.Errorf("egress: none must cut the box's network (expected 000):\n%s", got)
	}
}
