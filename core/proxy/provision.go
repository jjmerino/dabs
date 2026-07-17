// Package proxy is the host-side provisioner for a box's proxy egress: the
// dabs-side adapter between the box lifecycle and the egressforwarder machinery
// (the engine and the forwarder binary). It is NOT a verb and NOT a driver — it
// plays a driver-like role for egress, called by the recipe lifecycle: given a
// box's `proxies:` chain it starts the engine, mints/materializes the CA and
// forwarder, and returns the env, mounts, and Spec fields the driver needs;
// and it reaps the engine when the box comes down. Everything vendor-specific
// (interception, chains) lives in egressforwarder; this is only the wiring.
package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/egressforwarder/engine"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// caBoxDir is where the engine's public-cert directory is mounted in the box (a
// DIRECTORY, not a file — the apple micro-VM cannot bind a single file); the
// CA-trust env vars point every ecosystem's trust store at the cert inside it.
const (
	caBoxDir  = "/run/dabs/pub"
	caBoxPath = "/run/dabs/pub/ca.crt"
)

// Provisioned is what a provisioned proxy hands back to the box lifecycle. It
// exposes only scalars the caller needs, so the lifecycle never depends on the
// engine type: Env and Mounts feed the driver Spec; Socket is Spec.ProxySock;
// ForwarderBin is Spec.ForwarderBin; PID/Dir are recorded on the box node so a
// later Reap (from any dabs process) can stop the engine.
type Provisioned struct {
	Env          map[string]string
	Mounts       []sandbox.Mount
	Socket       string
	ForwarderBin string
	PID          int
	Dir          string
}

// Provision starts the host-side proxy for a box whose recipe declares a
// mapping egress: it adapts the allow/deny patterns and the http_proxy chain to
// the engine's config, starts the engine, mints/materializes the CA and
// forwarder, and returns what the driver and box need. The driver must enforce
// proxy egress — a box that asked for a proxied wall is never silently left
// open. expandPath resolves ~ and $VAR in module paths (the caller owns path
// semantics); recipeEnv is the recipe's own env, which wins over the proxy/CA
// env on conflict.
func Provision(drv sandbox.Driver, recipeName string, egress recipe.Egress, recipeEnv map[string]string, expandPath func(string) (string, error)) (*Provisioned, error) {
	ee, ok := drv.(sandbox.EgressEnforcer)
	if !ok {
		return nil, fmt.Errorf("recipe %q: target driver cannot enforce proxy egress — refusing to boot", recipeName)
	}
	if err := ee.CheckEgress(sandbox.EgressProxy); err != nil {
		return nil, fmt.Errorf("recipe %q: target driver cannot enforce proxy egress: %w", recipeName, err)
	}
	chain, err := buildChain(recipeName, egress.HTTPProxy, expandPath)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "dabs-proxy-")
	if err != nil {
		return nil, err
	}
	eng, err := engine.Start(dir, engine.Policy{Allow: egress.Allow, Deny: egress.Deny}, chain)
	if err != nil {
		// The engine never bound, so no box node will carry its dir for a later
		// Reap — clean the temp dir here or it leaks, one per failed boot.
		os.RemoveAll(dir)
		return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
	}
	// The forwarder binary lands in the engine's dir (reaped with it) and is
	// mounted into the box by the driver. It comes from dabs's embedded copy; a
	// dabs built without it fails here rather than booting an open box.
	forwarderBin, err := forwarder.Materialize(eng.Dir)
	if err != nil {
		eng.Stop()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("recipe %q: %w", recipeName, err)
	}
	loopback := fmt.Sprintf("http://127.0.0.1:%d", forwarder.Port)
	env := map[string]string{}
	for k, v := range recipeEnv {
		env[k] = v
	}
	for k, v := range map[string]string{
		// Every proxy-reading program → the in-box forwarder; loopback stays direct.
		"HTTP_PROXY": loopback, "http_proxy": loopback,
		"HTTPS_PROXY": loopback, "https_proxy": loopback,
		"NO_PROXY": "localhost,127.0.0.1", "no_proxy": "localhost,127.0.0.1",
		// Trust the engine's CA for the terminated TLS leg, across ecosystems.
		// git ignores CURL_CA_BUNDLE (libcurl sets CAINFO itself) → GIT_SSL_CAINFO.
		"SSL_CERT_FILE": caBoxPath, "CURL_CA_BUNDLE": caBoxPath,
		"NODE_EXTRA_CA_CERTS": caBoxPath, "REQUESTS_CA_BUNDLE": caBoxPath,
		"GIT_SSL_CAINFO": caBoxPath,
		// node's built-in https/fetch ignore HTTP(S)_PROXY unless asked (node 24+).
		"NODE_USE_ENV_PROXY": "1",
	} {
		if _, set := env[k]; !set { // the recipe's own env wins on conflict
			env[k] = v
		}
	}
	mounts := []sandbox.Mount{{Host: eng.CAPubDir, Path: caBoxDir, RO: true}}
	return &Provisioned{
		Env: env, Mounts: mounts, Socket: eng.Socket,
		ForwarderBin: forwarderBin, PID: eng.PID, Dir: eng.Dir,
	}, nil
}

// buildChain turns a recipe's ordered hops into the engine's config, expanding
// each custom executor's module path to an absolute path the engine can import.
// The hook's own config is every key except `module` (which names the hook),
// passed flat — so `- inner: {module: …, log: …}` hands the executor `{log: …}`.
func buildChain(recipeName string, hops []recipe.ProxyHop, expandPath func(string) (string, error)) ([]engine.HopConfig, error) {
	chain := make([]engine.HopConfig, 0, len(hops))
	for _, h := range hops {
		if h.IsTLS() {
			chain = append(chain, engine.HopConfig{TLS: h.TLS, Domains: h.Domains, FailOpen: h.FailOpen})
			continue
		}
		// A module hop: its engine identity is its Label (name or basename); the
		// hook's own config is everything except the `name` key.
		var cfg map[string]interface{}
		for k, v := range h.Config {
			if k == "name" {
				continue
			}
			if cfg == nil {
				cfg = map[string]interface{}{}
			}
			cfg[k] = v
		}
		expanded, err := expandPath(h.Module)
		if err != nil {
			return nil, fmt.Errorf("recipe %q: proxy module %s: %w", recipeName, h.Module, err)
		}
		// The engine imports the module from ITS temp dir, so a relative path must
		// be made absolute against the cwd here — otherwise the os.Stat below (cwd-
		// relative) passes while the engine's import (temp-dir-relative) fails. A
		// path-loaded recipe already anchored its modules on the yaml dir; this
		// handles a project ./dabs.yaml selected by name (relative → cwd).
		if !filepath.IsAbs(expanded) {
			abs, aerr := filepath.Abs(expanded)
			if aerr != nil {
				return nil, fmt.Errorf("recipe %q: proxy module %q: %w", recipeName, h.Module, aerr)
			}
			expanded = abs
		}
		// Catch a wrong path here with a clear message, rather than letting the
		// engine fail to boot on an unimportable module (a socket-timeout the
		// user cannot read).
		if _, err := os.Stat(expanded); err != nil {
			return nil, fmt.Errorf("recipe %q: proxy module %q not found at %s", recipeName, h.Module, expanded)
		}
		chain = append(chain, engine.HopConfig{Name: h.Label(), Module: expanded, Config: cfg})
	}
	return chain, nil
}

// Reap stops a box's proxy engine (its whole process group — the engine leads
// its own session) and removes its temp dir. pid/dir come from the box node, so
// this works from any dabs process, including a `dabs rm` that never started the
// engine. A zero pid means the box had no proxy. Call it BEFORE the node record
// is removed, or the pid is lost.
func Reap(pid int, dir string) {
	if pid == 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
