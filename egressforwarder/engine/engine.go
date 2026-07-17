// Package engine is the host-side launcher for the dabs proxy engine — the Bun
// program (engine.ts, embedded) that runs a recipe's ordered `proxies:` chain.
// When a box requests proxy egress, dabs Start()s an engine bound to a unix
// socket, mints a CA the box will trust, and hands the driver that socket; the
// in-box forwarder bridges the box's HTTP_PROXY to it. Go owns the lifecycle
// and the config; all interception logic lives in engine.ts and the hook
// modules it loads.
package engine

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed engine.ts
var engineTS []byte

// HopConfig is one resolved chain entry handed to the engine: a built-in
// directive (tls/egress) or a custom executor with an absolute module path.
type HopConfig struct {
	Name    string                 `json:"name,omitempty"`    // a module hop's label
	TLS     string                 `json:"tls,omitempty"`     // "terminate" | "originate" for a tls hop
	Domains []string               `json:"domains,omitempty"` // on terminate: only these hosts are decrypted
	Module  string                 `json:"module,omitempty"`  // a module hop's path
	Config  map[string]interface{} `json:"config,omitempty"`
}

// engineConfig is the JSON the engine reads on boot.
type engineConfig struct {
	Socket string      `json:"socket"`
	CADir  string      `json:"caDir"`
	Chain  []HopConfig `json:"chain"`
}

// Engine is a running proxy engine: its box-facing socket, the CA cert the box
// must trust, the working dir (temp files to reap), the process group id, and a
// stop to end it. PID/Dir are recorded on the box node so a later `dabs rm`
// process — which does not hold this object — can reap the engine too.
type Engine struct {
	Socket   string
	CACert   string // caDir/ca.crt (reference)
	CAPubDir string // caDir/pub — a dir holding ONLY ca.crt, safe to mount into the box
	Dir      string
	PID      int
	cmd      *exec.Cmd
}

// Start materializes the embedded engine into dir, launches it under `bun`, and
// waits for its socket to come up (the engine mints the CA before binding, so a
// live socket means the CA cert exists too). The caller mounts CACert into the
// box and points the box's HTTP_PROXY at the in-box forwarder.
func Start(dir string, chain []HopConfig) (*Engine, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	enginePath := filepath.Join(dir, "engine.ts")
	if err := os.WriteFile(enginePath, engineTS, 0o600); err != nil {
		return nil, err
	}
	caDir := filepath.Join(dir, "ca")
	socket := filepath.Join(dir, "engine.sock")
	cfgPath := filepath.Join(dir, "config.json")
	cfg, err := json.Marshal(engineConfig{Socket: socket, CADir: caDir, Chain: chain})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		return nil, err
	}

	// The engine is a daemon: it outlives this call and runs for the box's life.
	// Its stdio must NOT inherit dabs's — a detached child holding dabs's stderr
	// keeps that pipe open, hanging any caller that reads dabs's output. Log to a
	// file (a debugging trail) and start a new session so it fully detaches.
	logPath := filepath.Join(dir, "engine.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("bun", enginePath, cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start proxy engine (bun must be on PATH): %w", err)
	}
	for i := 0; i < 200; i++ { // up to ~10s for the socket to appear
		if _, e := os.Stat(socket); e == nil {
			relayWarnings(logPath) // surface boot warnings (e.g. a hook outside a tls window)
			return &Engine{Socket: socket, CACert: filepath.Join(caDir, "ca.crt"), CAPubDir: filepath.Join(caDir, "pub"), Dir: dir, PID: cmd.Process.Pid, cmd: cmd}, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	// The engine died before binding — its log holds the real reason (a module
	// that would not import, a bad chain). Surface it instead of a bare timeout.
	return nil, fmt.Errorf("proxy engine did not start: %s", engineLogTail(logPath))
}

// engineLogTail returns the last few non-empty lines of the engine log, for an
// error message — the engine's own diagnostic beats a socket-timeout.
func engineLogTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no engine log)"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}

// relayWarnings prints any `warning:` lines the engine logged at boot to dabs's
// stderr, so a helpful diagnostic (a hook outside a tls window) is visible where
// the user looks, not buried in the engine log.
func relayWarnings(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "warning:") {
			fmt.Fprintf(os.Stderr, "proxy %s\n", strings.TrimSpace(line))
		}
	}
}

// Stop ends the engine process.
func (e *Engine) Stop() {
	if e != nil && e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
}
