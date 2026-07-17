//go:build e2e

// Credential-injection end-to-end, for the Claude case. A box holds only DUMMY
// OAuth tokens; its ONLY egress is a dabs proxy running the real credential
// broker (contrib/proxy/creds-inject-anthropic/broker.ts). The Go test drives an in-box curl client
// request-by-request via `dabs exec` — the login token exchange, then the first
// streaming message — and a terminal responder hop plays Anthropic, so `tls:
// originate` is never reached and the test CANNOT hit the real network even if
// the swap breaks.
//
// It verifies the two guarantees:
//
//	swap        outbound, the box's DUMMY Bearer reaches "Anthropic" as the REAL
//	            token; inbound, a login response carrying REAL tokens reaches the
//	            box as DUMMIES (and the real rotation is captured host-side).
//	non-block   the streaming /v1/messages response round-trips to the box in
//	            full, without hanging (curl --max-time returns the last event).
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The terminal responder: plays Anthropic. It records the Authorization it was
// handed (AFTER the broker swapped it), returns rotated REAL tokens for the login
// exchange, and a small SSE stream for a message. It answers every path, so the
// chain never originates to the real internet.
const credResponder = `import { appendFileSync } from "node:fs";
export default (config) => ({
  onRequest(head) {
    appendFileSync(config.log, head.method + " " + head.path + " auth=" + (head.headers["authorization"] || "") + "\n");
    if (head.path === "/v1/oauth/token") {
      return { action: "respond", status: 200, headers: { "content-type": "application/json" },
        body: JSON.stringify({ token_type: "Bearer", access_token: config.rotAccess, refresh_token: config.rotRefresh, expires_in: 28800 }) };
    }
    if (head.path === "/v1/messages") {
      return { action: "respond", status: 200, headers: { "content-type": "text/event-stream" },
        body: "event: start\ndata: first\n\ndata: middle\n\ndata: message_stop\n\n" };
    }
    return { action: "respond", status: 200, body: "ok" };
  },
});
`

func TestCredentialInjectionAndNonBlocking(t *testing.T) {
	clean(t)

	// Tokens. The DUMMY sentinels MUST equal broker.ts's (sk-ant-o(at|rt)01- +
	// "DABSBROKERDUMMY" padded to 108). The REAL/ROT tokens are distinctive so the
	// assertions can tell "reached Anthropic" from "reached the box".
	dummyAccess := "sk-ant-oat01-DABSBROKERDUMMY" + strings.Repeat("0", 80)
	realAccess := "sk-ant-oat01-E2EREALaccessXXXXXXXXXXXXXXXXXXXX"
	realRefresh := "sk-ant-ort01-E2EREALrefreshXXXXXXXXXXXXXXXXXXXX"
	rotAccess := "sk-ant-oat01-E2EROTaccessYYYYYYYYYYYYYYYYYYYY"
	rotRefresh := "sk-ant-ort01-E2EROTrefreshYYYYYYYYYYYYYYYYYYYY"

	dir, err := os.MkdirTemp(home, "credinject-")
	if err != nil {
		t.Fatal(err)
	}
	respLog := filepath.Join(dir, "responder.log")
	vault := filepath.Join(dir, "vault.json")

	write := func(path, content string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}

	// The host-side vault: the REAL token, the only place it lives. The broker
	// injects it outbound and never lets it reach the box.
	write(vault, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q}}`, realAccess, realRefresh), 0o600)

	// The module under test is the REAL broker, copied in verbatim so the e2e
	// exercises the shipped file (it imports only node:fs — self-contained).
	brokerSrc, err := os.ReadFile("../../contrib/proxy/creds-inject-anthropic/broker.ts")
	if err != nil {
		t.Fatalf("read broker.ts: %v", err)
	}
	write(filepath.Join(dir, "broker.ts"), string(brokerSrc), 0o644)
	write(filepath.Join(dir, "responder.ts"), credResponder, 0o644)

	// The recipe: box egress is proxy-only; the chain is terminate → REAL broker →
	// terminal responder → originate. The responder answers everything, so the
	// window never originates to the internet — egress is effectively disabled.
	yaml := fmt.Sprintf(`default: credinject
recipes:
  credinject:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - { module: %s/broker.ts, vault: %s }
        - { module: %s/responder.ts, log: %s, rotAccess: %s, rotRefresh: %s }
        - tls: originate
`, dir, vault, dir, respLog, rotAccess, rotRefresh)
	write(filepath.Join(dir, "dabs.yaml"), yaml, 0o644)

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst := instanceLine(t, out)

	// 1) The login token exchange: the response carries REAL tokens; the box must
	// see DUMMIES. Driven straight through `dabs exec` — the box's proxy applies.
	login, code := run(fmt.Sprintf(`dabs exec %s -- curl -s --max-time 10 -X POST https://platform.claude.com/v1/oauth/token -H 'content-type: application/json' -d '{"grant_type":"authorization_code","code":"abc"}'`, inst))
	if code != 0 {
		t.Fatalf("login request failed (%d):\n%s", code, login)
	}
	// 2) The first message (SSE): the box sends a DUMMY Bearer; "Anthropic" must
	// get the REAL one. --max-time turns a hang into a truncated (no last-event) body.
	msg, code := run(fmt.Sprintf(`dabs exec %s -- curl -sN --max-time 10 -X POST https://api.anthropic.com/v1/messages -H 'authorization: Bearer %s' -H 'content-type: application/json' -d '{"stream":true}'`, inst, dummyAccess))
	if code != 0 {
		t.Fatalf("message request failed (%d):\n%s", code, msg)
	}

	respSeen := readFile(t, respLog)

	// --- swap, outbound: the box sent a DUMMY Bearer; the responder saw the REAL
	// token. The login ran first and its response ROTATED the credential (the
	// broker captured rotAccess), so the message injects the rotated token — proof
	// the swap tracks rotation, not a stale seed.
	if !strings.Contains(respSeen, "/v1/messages auth=Bearer "+rotAccess) {
		t.Errorf("outbound injection failed: responder did not receive the current REAL token on /v1/messages.\nresponder log:\n%s", respSeen)
	}
	if strings.Contains(respSeen, dummyAccess) {
		t.Errorf("the DUMMY token reached the responder un-swapped — injection did not run.\nresponder log:\n%s", respSeen)
	}

	// --- swap, inbound: the login response carried REAL rotated tokens; the box got DUMMIES.
	if strings.Contains(login, rotAccess) || strings.Contains(login, rotRefresh) {
		t.Errorf("a REAL token leaked into the box — inbound scrub failed.\nlogin response:\n%s", login)
	}
	if !strings.Contains(login, dummyAccess) {
		t.Errorf("box did not receive the DUMMY-scrubbed login response.\nlogin response:\n%s", login)
	}

	// --- non-blocking: the streamed message arrived in full (the last event is present).
	if !strings.Contains(msg, "message_stop") {
		t.Errorf("box did not receive the full SSE stream (missing the last event — a hang).\nmessage response:\n%s", msg)
	}

	// --- capture: the real rotation was learned host-side, never in the box.
	if v := readFile(t, vault); !strings.Contains(v, rotAccess) {
		t.Errorf("the rotated REAL token was not captured to the host vault.\nvault:\n%s", v)
	}
}
