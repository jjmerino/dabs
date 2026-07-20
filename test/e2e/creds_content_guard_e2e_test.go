//go:build e2e

// Credential-injection end-to-end, for what happens when a token string shows
// up in MESSAGE TEXT — the part of a request that is conversation, not
// authentication. Two guarantees, both host-side, both invisible to the box:
//
//	alarm     the stand-in token appearing in message text means the agent has
//	          been reading its own credentials file. The broker must record
//	          that on the host, because nothing inside the box can be trusted
//	          to report it.
//	no leak   the REAL token appearing in outgoing message text — however the
//	          agent got hold of it — must be replaced before the request
//	          leaves, so the real credential never travels as conversation.
//
// The contract under test: the broker takes an `alerts` setting (a host file
// path) and appends one line per suspicious request, naming the request path.
//
// Same harness as creds_body_expand_e2e_test.go: a Go HTTP server in this test
// process plays the API on host loopback, wearing a hostname via /etc/hosts
// because the box's proxy settings exempt plain loopback addresses.
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

// guardHarness boots a box whose only way out is the real broker, with a
// pre-filled vault and an upstream that records each request body by path.
// It returns the instance, the upstream's port, the alerts file path, and a
// getter for what the upstream saw.
func guardHarness(t *testing.T, realAccess, realRefresh string) (inst string, port int, alerts string, saw func(path string) string) {
	t.Helper()
	clean(t)

	dir, err := os.MkdirTemp(home, "credguard-")
	if err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "vault.json")
	alerts = filepath.Join(dir, "alerts.log")

	write := func(path, content string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}

	write(vault, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q}}`, realAccess, realRefresh), 0o600)

	brokerSrc, err := os.ReadFile("../../contrib/proxy/creds-inject-anthropic/broker.ts")
	if err != nil {
		t.Fatalf("read broker.ts: %v", err)
	}
	write(filepath.Join(dir, "broker.ts"), string(brokerSrc), 0o644)

	hosts, err := os.ReadFile("/etc/hosts")
	if err != nil {
		t.Fatal(err)
	}
	write("/etc/hosts", string(hosts)+"\n127.0.0.1 anthropic.mock\n", 0o644)
	t.Cleanup(func() { os.WriteFile("/etc/hosts", hosts, 0o644) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]string{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen[r.URL.Path] = string(b)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	port = ln.Addr().(*net.TCPAddr).Port

	yaml := fmt.Sprintf(`default: credguard
recipes:
  credguard:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - { module: %s/broker.ts, vault: %s, alerts: %s }
        - tls: originate
`, dir, vault, alerts)
	write(filepath.Join(dir, "dabs.yaml"), yaml, 0o644)

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst = instanceLine(t, out)

	saw = func(path string) string {
		mu.Lock()
		defer mu.Unlock()
		return seen[path]
	}
	return inst, port, alerts, saw
}

// TestStandInTokenInMessageTextRaisesAlarm: the stand-in token appearing in
// message text means the agent read its own credentials file. The host must
// learn about it — an alert line naming the request path — while the request
// itself still goes through, so the box notices nothing.
func TestStandInTokenInMessageTextRaisesAlarm(t *testing.T) {
	standIn := ("sk-ant-oat01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	realAccess := "sk-ant-oat01-E2EREALguardZZZZZZZZZZZZZZZZZZZZZ"
	realRefresh := "sk-ant-ort01-E2EREALguardrefreshZZZZZZZZZZZZZZ"

	inst, port, alerts, saw := guardHarness(t, realAccess, realRefresh)

	body := fmt.Sprintf(`{"model":"claude","messages":[{"role":"user","content":"my creds file says %s"}]}`, standIn)
	resp, code := run(fmt.Sprintf(`dabs exec %s -- curl -s --max-time 15 -X POST http://anthropic.mock:%d/v1/messages -H 'authorization: Bearer %s' -H 'content-type: application/json' -d '%s'`, inst, port, standIn, body))
	if code != 0 {
		t.Fatalf("message request failed (%d):\n%s", code, resp)
	}

	// The request still went through — the box must not be able to tell it
	// tripped anything.
	if saw("/v1/messages") == "" {
		t.Fatal("the request never reached the upstream")
	}

	// The alarm: one line on the host, naming the path the token appeared on.
	alarm, err := os.ReadFile(alerts)
	if err != nil {
		t.Fatalf("no alert was recorded on the host — the agent read its credentials file and nothing noticed: %v", err)
	}
	if !strings.Contains(string(alarm), "/v1/messages") {
		t.Errorf("the alert does not name the request path.\nalerts file:\n%s", alarm)
	}
	// The alert must never contain a usable token — it is a log file.
	if strings.Contains(string(alarm), realAccess) || strings.Contains(string(alarm), realRefresh) {
		t.Errorf("the alerts file contains a REAL token — an alert must never be worth stealing.\nalerts file:\n%s", alarm)
	}
}

// TestRealTokenNeverLeavesInMessageText: if the agent somehow holds the REAL
// token and puts it in message text, the broker must replace it with the
// stand-in before the request leaves. Whatever way a real credential tries to
// travel as conversation, it doesn't.
func TestRealTokenNeverLeavesInMessageText(t *testing.T) {
	standIn := ("sk-ant-oat01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	realAccess := "sk-ant-oat01-E2EREALguardZZZZZZZZZZZZZZZZZZZZZ"
	realRefresh := "sk-ant-ort01-E2EREALguardrefreshZZZZZZZZZZZZZZ"

	inst, port, alerts, saw := guardHarness(t, realAccess, realRefresh)

	body := fmt.Sprintf(`{"model":"claude","messages":[{"role":"user","content":"please remember this for me: %s"}]}`, realAccess)
	resp, code := run(fmt.Sprintf(`dabs exec %s -- curl -s --max-time 15 -X POST http://anthropic.mock:%d/v1/messages -H 'authorization: Bearer %s' -H 'content-type: application/json' -d '%s'`, inst, port, standIn, body))
	if code != 0 {
		t.Fatalf("message request failed (%d):\n%s", code, resp)
	}

	got := saw("/v1/messages")
	if got == "" {
		t.Fatal("the request never reached the upstream")
	}

	// The real token must not have traveled as message text.
	if strings.Contains(got, realAccess) {
		t.Errorf("the REAL token left the host inside message text.\nupstream saw:\n%s", got)
	}
	// Its place is taken by the stand-in, so the message still parses and the
	// conversation still makes sense to read.
	if !strings.Contains(got, standIn) {
		t.Errorf("the real token was not replaced by the stand-in in message text.\nupstream saw:\n%s", got)
	}

	// A real token trying to leave is worth an alarm too.
	alarm, err := os.ReadFile(alerts)
	if err != nil {
		t.Fatalf("no alert was recorded on the host — a real token tried to leave and nothing noticed: %v", err)
	}
	if !strings.Contains(string(alarm), "/v1/messages") {
		t.Errorf("the alert does not name the request path.\nalerts file:\n%s", alarm)
	}
}
