//go:build e2e

// Credential-injection end-to-end, for the request-BODY scope. The broker must
// expand dummy→real ONLY where a legitimate request needs the real credential:
//
//	Authorization header    any api request       — the token authenticates it
//	request body            /v1/oauth/token only  — the refresh grant carries
//	                                                the refresh token in JSON
//
// A /v1/messages body is CONTENT, not credentials. Once an in-box agent reads
// its own creds file, the dummy string lands in the conversation transcript,
// and the next /v1/messages request carries it inside message content. If the
// broker expands there, the REAL token is handed to the model in its context —
// the exact leak the dummy exists to prevent. That body must pass through
// unchanged.
//
// The header-swap sibling (creds_inject_anthropic_e2e_test.go) uses a terminal
// responder, but a responder answers at onRequest and the engine then DRAINS
// the request body without running onRequestChunk — a terminal chain can never
// observe a body transform. So this test forwards for real: the box speaks
// plain http:// to a Go HTTP server in this test process, reached through
// `tls: originate` on loopback.
//
// The box's proxy env carries NO_PROXY=localhost,127.0.0.1, so the upstream
// must wear a hostname or curl would bypass the proxy entirely. The engine
// resolves upstream hosts on ITS side, so an /etc/hosts entry here (the suite
// runs as root in its own disposable box) points the name at loopback.
package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCredentialBodySwapScope(t *testing.T) {
	clean(t)

	// The DUMMY sentinels MUST equal broker.ts's (sk-ant-o(at|rt)01- +
	// "DABSBROKERDUMMY" padded to 108). The REAL tokens are distinctive so the
	// assertions can tell "expanded" from "passed through".
	dummyAccess := ("sk-ant-oat01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	dummyRefresh := ("sk-ant-ort01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	realAccess := "sk-ant-oat01-E2EREALbodyZZZZZZZZZZZZZZZZZZZZZZ"
	realRefresh := "sk-ant-ort01-E2EREALbodyrefreshZZZZZZZZZZZZZZZ"

	dir, err := os.MkdirTemp(home, "credscope-")
	if err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "vault.json")

	write := func(path, content string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}

	// The vault is pre-seeded: this journey starts logged in, no exchange step.
	write(vault, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q}}`, realAccess, realRefresh), 0o600)

	// The module under test is the REAL broker, copied in verbatim.
	brokerSrc, err := os.ReadFile("../../contrib/proxy/creds-inject-anthropic/broker.ts")
	if err != nil {
		t.Fatalf("read broker.ts: %v", err)
	}
	write(filepath.Join(dir, "broker.ts"), string(brokerSrc), 0o644)

	// The engine resolves "anthropic.mock" via the host's /etc/hosts; restore it
	// after so later tests see the file they expect.
	hosts, err := os.ReadFile("/etc/hosts")
	if err != nil {
		t.Fatal(err)
	}
	write("/etc/hosts", string(hosts)+"\n127.0.0.1 anthropic.mock\n", 0o644)
	t.Cleanup(func() { os.WriteFile("/etc/hosts", hosts, 0o644) })

	// The upstream: plays Anthropic on host loopback, recording what each
	// request looked like AFTER the broker — per path, the Authorization it
	// carried and the body it delivered.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]string{} // path → "auth=… body=…"
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen[r.URL.Path] = "auth=" + r.Header.Get("Authorization") + " body=" + string(b)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	yaml := fmt.Sprintf(`default: credscope
recipes:
  credscope:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - { module: %s/broker.ts, vault: %s }
        - tls: originate
`, dir, vault)
	write(filepath.Join(dir, "dabs.yaml"), yaml, 0o644)

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)

	// 1) A message whose CONTENT quotes the dummy token: the transcript case —
	// the agent cat'd its creds file last turn and Claude Code now sends the
	// whole conversation as the request body.
	transcript := fmt.Sprintf(`{"model":"claude","messages":[{"role":"user","content":"my creds file says %s"}]}`, dummyAccess)
	msg, code := run(fmt.Sprintf(`dabs exec %s -- curl -s --max-time 15 -X POST http://anthropic.mock:%d/v1/messages -H 'authorization: Bearer %s' -H 'content-type: application/json' -d '%s'`, inst, port, dummyAccess, transcript))
	if code != 0 {
		t.Fatalf("message request failed (%d):\n%s", code, msg)
	}

	// 2) The refresh grant: the ONE request whose body legitimately carries a
	// credential — the dummy refresh token, which must reach Anthropic real.
	refreshBody := fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":"%s"}`, dummyRefresh)
	refresh, code := run(fmt.Sprintf(`dabs exec %s -- curl -s --max-time 15 -X POST http://anthropic.mock:%d/v1/oauth/token -H 'content-type: application/json' -d '%s'`, inst, port, refreshBody))
	if code != 0 {
		t.Fatalf("refresh request failed (%d):\n%s", code, refresh)
	}

	mu.Lock()
	msgSeen := seen["/v1/messages"]
	refreshSeen := seen["/v1/oauth/token"]
	mu.Unlock()

	// --- the message: the header swap is legitimate; the BODY is content and
	// must pass through untouched. A real token inside message content is
	// handed straight to the model — the leak the dummy exists to prevent.
	if !strings.Contains(msgSeen, "auth=Bearer "+realAccess) {
		t.Errorf("the Authorization header on /v1/messages was not swapped to the REAL token.\nupstream saw:\n%s", msgSeen)
	}
	if strings.Contains(msgSeen, "body=") && strings.Contains(strings.SplitN(msgSeen, "body=", 2)[1], realAccess) {
		t.Errorf("the REAL token was injected into /v1/messages CONTENT — it reaches the model's context.\nupstream saw:\n%s", msgSeen)
	}
	if !strings.Contains(strings.SplitN(msgSeen, "body=", 2)[1], dummyAccess) {
		t.Errorf("the /v1/messages body did not pass through unchanged (the dummy is gone).\nupstream saw:\n%s", msgSeen)
	}

	// --- the refresh: the body swap is the legitimate case and must still work.
	if !strings.Contains(refreshSeen, realRefresh) {
		t.Errorf("the refresh grant did not carry the REAL refresh token — the legitimate body swap broke.\nupstream saw:\n%s", refreshSeen)
	}
	if strings.Contains(refreshSeen, dummyRefresh) {
		t.Errorf("the DUMMY refresh token reached Anthropic un-swapped.\nupstream saw:\n%s", refreshSeen)
	}
}
