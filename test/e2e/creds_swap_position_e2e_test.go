//go:build e2e

// Credential-injection end-to-end, for the exact POSITIONS where the stand-in
// token may become the real one. There are exactly two, and each is defined by
// name, place, and host — not by what the bytes look like:
//
//	the `Authorization` header, value `Bearer <token>`,
//	    on requests to a named host
//	the `refresh_token` field of the JSON body,
//	    on POST /v1/oauth/token to a named host
//
// Everything that ALMOST matches must stay untouched: a header whose name is
// off by one letter, a body field named `refresh_tokens`, the right field on
// the wrong path, the right request to the wrong host. And whatever else the
// request carries, the real token must never travel inside message text.
//
// The contract under test: the broker takes a `hosts` setting — the list of
// hosts whose requests may carry credentials — and swaps only in the two
// positions above, only on those hosts.
//
// Same harness family as the sibling tests: a Go HTTP server in this test
// process plays the upstream on host loopback (curl's --noproxy '' keeps the
// loopback URL on the proxy path). The harness takes the broker's `hosts`
// setting, so the upstream can be the allowed host in one test and a stranger
// in another — no DNS involved.
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

type wireRecord struct {
	host    string // as requested, without the port
	path    string
	headers http.Header
	body    string
}

// positionHarness boots a box whose only way out is the real broker, with
// `hosts` as the broker's list of hosts that may carry credentials. It returns
// the instance, the upstream's port, and a snapshot getter for every request
// the upstream received, in order.
func positionHarness(t *testing.T, realAccess, realRefresh, hosts string) (inst string, port int, records func() []wireRecord) {
	t.Helper()
	clean(t)

	dir, err := os.MkdirTemp(home, "credpos-")
	if err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "vault.json")

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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []wireRecord
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, wireRecord{
			host:    strings.Split(r.Host, ":")[0],
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    string(b),
		})
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	port = ln.Addr().(*net.TCPAddr).Port

	yaml := fmt.Sprintf(`default: credpos
recipes:
  credpos:
    image: dabs-e2e
    keep: true
    egress:
      http_proxy:
        - tls: terminate
        - { module: %s/broker.ts, vault: %s, hosts: [%s] }
        - tls: originate
`, dir, vault, hosts)
	write(filepath.Join(dir, "dabs.yaml"), yaml, 0o644)

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("boot failed (%d):\n%s", code, out)
	}
	inst = instanceLine(t, out)

	records = func() []wireRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]wireRecord(nil), seen...)
	}
	return inst, port, records
}

// post sends one request from inside the box and fails the test if the send
// itself breaks. extraHeader is a full "Name: value" header line, or empty.
func post(t *testing.T, inst string, url, extraHeader, body string) {
	t.Helper()
	h := ""
	if extraHeader != "" {
		h = fmt.Sprintf("-H '%s' ", extraHeader)
	}
	out, code := run(fmt.Sprintf(`dabs exec %s -- curl -s --noproxy '' --max-time 15 -X POST %s %s-H 'content-type: application/json' -d '%s'`, inst, url, h, body))
	if code != 0 {
		t.Fatalf("request to %s failed (%d):\n%s", url, code, out)
	}
}

// noRealInMessageText fails if the real token ever traveled inside a message
// body — whatever the test was otherwise about.
func noRealInMessageText(t *testing.T, records []wireRecord, realAccess, realRefresh string) {
	t.Helper()
	for _, r := range records {
		if r.path != "/v1/messages" {
			continue
		}
		if strings.Contains(r.body, realAccess) || strings.Contains(r.body, realRefresh) {
			t.Errorf("a REAL token traveled inside message text on %s%s.\nbody:\n%s", r.host, r.path, r.body)
		}
	}
}

// TestHeaderSwapNeedsTheExactHeaderName: `Authorization: Bearer <stand-in>` on
// the named host becomes real; a header whose name is one letter short stays
// the stand-in — the swap keys on the header's name, not on its value looking
// like a token.
func TestHeaderSwapNeedsTheExactHeaderName(t *testing.T) {
	standIn := ("sk-ant-oat01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	realAccess := "sk-ant-oat01-E2EREALposZZZZZZZZZZZZZZZZZZZZZZZ"
	realRefresh := "sk-ant-ort01-E2EREALposrefreshZZZZZZZZZZZZZZZZ"

	inst, port, records := positionHarness(t, realAccess, realRefresh, "127.0.0.1")
	api := fmt.Sprintf("http://127.0.0.1:%d", port)

	post(t, inst, api+"/v1/messages", "Authorization: Bearer "+standIn, `{"messages":[{"role":"user","content":"hello"}]}`)
	post(t, inst, api+"/v1/messages", "Authorizatio: Bearer "+standIn, `{"messages":[{"role":"user","content":"hello"}]}`)

	recs := records()
	if len(recs) != 2 {
		t.Fatalf("expected 2 requests at the upstream, got %d", len(recs))
	}

	if got := recs[0].headers.Get("Authorization"); got != "Bearer "+realAccess {
		t.Errorf("the real Authorization header was not swapped to the REAL token.\ngot: %s", got)
	}
	if got := recs[1].headers.Get("Authorizatio"); got != "Bearer "+standIn {
		t.Errorf("a header that is NOT `Authorization` was treated as one — the swap must key on the exact header name.\ngot: %s", got)
	}
	noRealInMessageText(t, recs, realAccess, realRefresh)
}

// TestBodySwapNeedsTheExactFieldPathAndRequest: the `refresh_token` field on
// POST /v1/oauth/token becomes real. The same value under a field named
// `refresh_tokens`, or the right field name on /v1/messages, stays the
// stand-in — the swap keys on field name AND request, not on the value.
func TestBodySwapNeedsTheExactFieldPathAndRequest(t *testing.T) {
	standInRefresh := ("sk-ant-ort01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	realAccess := "sk-ant-oat01-E2EREALposZZZZZZZZZZZZZZZZZZZZZZZ"
	realRefresh := "sk-ant-ort01-E2EREALposrefreshZZZZZZZZZZZZZZZZ"

	inst, port, records := positionHarness(t, realAccess, realRefresh, "127.0.0.1")
	api := fmt.Sprintf("http://127.0.0.1:%d", port)

	post(t, inst, api+"/v1/oauth/token", "", fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":"%s"}`, standInRefresh))
	post(t, inst, api+"/v1/oauth/token", "", fmt.Sprintf(`{"grant_type":"refresh_token","refresh_tokens":"%s"}`, standInRefresh))
	post(t, inst, api+"/v1/messages", "", fmt.Sprintf(`{"messages":[{"role":"user","content":"x"}],"refresh_token":"%s"}`, standInRefresh))

	recs := records()
	if len(recs) != 3 {
		t.Fatalf("expected 3 requests at the upstream, got %d", len(recs))
	}

	if !strings.Contains(recs[0].body, fmt.Sprintf(`"refresh_token":%q`, realRefresh)) {
		t.Errorf("the refresh_token field on /v1/oauth/token was not swapped to the REAL token.\nbody:\n%s", recs[0].body)
	}
	if !strings.Contains(recs[1].body, standInRefresh) || strings.Contains(recs[1].body, realRefresh) {
		t.Errorf("a field named `refresh_tokens` was treated as the credential field — the swap must key on the exact field name.\nbody:\n%s", recs[1].body)
	}
	if !strings.Contains(recs[2].body, standInRefresh) || strings.Contains(recs[2].body, realRefresh) {
		t.Errorf("a `refresh_token` field on /v1/messages was swapped — the swap must key on the request, not just the field name.\nbody:\n%s", recs[2].body)
	}
	noRealInMessageText(t, recs, realAccess, realRefresh)
}

// TestSwapNeedsTheNamedHost: the exact right positions — Authorization header,
// refresh_token field on POST /v1/oauth/token — on a host the broker was NOT
// told about stay the stand-in. A credential is for the service it belongs to;
// any other host asking gets the stand-in and learns nothing.
func TestSwapNeedsTheNamedHost(t *testing.T) {
	standIn := ("sk-ant-oat01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	standInRefresh := ("sk-ant-ort01-" + "DABSBROKERDUMMY" + strings.Repeat("0", 108))[:108]
	realAccess := "sk-ant-oat01-E2EREALposZZZZZZZZZZZZZZZZZZZZZZZ"
	realRefresh := "sk-ant-ort01-E2EREALposrefreshZZZZZZZZZZZZZZZZ"

	// The broker only trusts a host the upstream is not — every request below
	// goes to a stranger.
	inst, port, records := positionHarness(t, realAccess, realRefresh, "api.anthropic.com")
	stranger := fmt.Sprintf("http://127.0.0.1:%d", port)

	post(t, inst, stranger+"/v1/messages", "Authorization: Bearer "+standIn, `{"messages":[{"role":"user","content":"hello"}]}`)
	post(t, inst, stranger+"/v1/oauth/token", "", fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":"%s"}`, standInRefresh))

	recs := records()
	if len(recs) != 2 {
		t.Fatalf("expected 2 requests at the upstream, got %d", len(recs))
	}

	if got := recs[0].headers.Get("Authorization"); got != "Bearer "+standIn {
		t.Errorf("the Authorization header was swapped for a host the broker was not told about.\ngot: %s", got)
	}
	if !strings.Contains(recs[1].body, standInRefresh) || strings.Contains(recs[1].body, realRefresh) {
		t.Errorf("the refresh_token field was swapped for a host the broker was not told about.\nbody:\n%s", recs[1].body)
	}
	noRealInMessageText(t, recs, realAccess, realRefresh)
}
