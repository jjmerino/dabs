package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startWithFakeBun runs Start with a `bun` on PATH that writes its
// environment next to the engine and creates the socket Start waits for. It
// returns the environment the engine was started with.
func startWithFakeBun(t *testing.T) map[string]string {
	t.Helper()
	bin := t.TempDir()
	fake := "#!/bin/sh\nd=$(dirname \"$1\")\nenv >\"$d/env.txt\"\ntouch \"$d/engine.sock\"\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	e, err := Start(dir, Policy{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(e.Stop)
	b, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatalf("the fake bun wrote no environment: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

func TestEngineStartsWithoutProxyVarsWhenAnUpstreamIsSet(t *testing.T) {
	for _, k := range proxyVars {
		t.Setenv(k, "http://127.0.0.1:9")
	}
	t.Setenv("DABS_UPSTREAM_PROXY", "http://127.0.0.1:18080")
	t.Setenv("NO_PROXY", "localhost")
	env := startWithFakeBun(t)
	for _, k := range proxyVars {
		if v, ok := env[k]; ok {
			t.Errorf("the engine was started with %s=%s; with an upstream it must have none", k, v)
		}
	}
	if got := env["DABS_UPSTREAM_PROXY"]; got != "http://127.0.0.1:18080" {
		t.Errorf("DABS_UPSTREAM_PROXY = %q, want it passed through", got)
	}
	if got := env["NO_PROXY"]; got != "localhost" {
		t.Errorf("NO_PROXY = %q, want the rest of the environment passed through", got)
	}
}

func TestEngineKeepsProxyVarsWithoutAnUpstream(t *testing.T) {
	for _, k := range proxyVars {
		t.Setenv(k, "http://127.0.0.1:9")
	}
	t.Setenv("DABS_UPSTREAM_PROXY", "")
	env := startWithFakeBun(t)
	for _, k := range proxyVars {
		if env[k] != "http://127.0.0.1:9" {
			t.Errorf("%s = %q; without an upstream the environment must be passed through unchanged", k, env[k])
		}
	}
}
