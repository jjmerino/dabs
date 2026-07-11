// Package server implements sandbox.Driver by proxying every verb to a REMOTE
// machine that has dabs installed, over ssh with pubkey auth (BatchMode —
// never prompts). A Mac mini or Linux box sitting around becomes a seamless
// sandbox host: the remote dabs picks its own local driver, this one just
// carries the conversation.
//
// Build ships the Dockerfile and context to a remote staging dir (like
// docker ships context to its daemon) and builds there; instances live
// entirely on the remote.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/jjmerino/dabs/core/sandbox"
)

// Driver proxies dabs verbs to a remote machine running dabs. The TRANSPORT
// (how we reach it) is decoupled from the server noun: ssh today, a future
// "dabs serve" daemon later. Only ssh is implemented; New rejects others.
type Driver struct {
	via  string
	host string

	// The remote dabs path is resolved lazily, once (non-login shells lack
	// ~/.local/bin in PATH, so we must locate it via a login shell). Lazy
	// so that an unreachable target does not break commands that never
	// touch it.
	once    sync.Once
	dabs    string
	dabsErr error
}

// New returns a driver reaching host over the named transport. Reachability
// and the remote dabs install are verified on first use, not here.
func New(via, host string) (*Driver, error) {
	if via != "" && via != "ssh" {
		return nil, fmt.Errorf("server: unsupported transport %q (only ssh today)", via)
	}
	if host == "" {
		return nil, fmt.Errorf(`server: empty "host"`)
	}
	if via == "" {
		via = "ssh"
	}
	return &Driver{via: via, host: host}, nil
}

// dabsPath resolves (once) where dabs lives on the remote.
func (d *Driver) dabsPath() (string, error) {
	d.once.Do(func() {
		out, err := exec.Command("ssh", "-o", "BatchMode=yes", d.host, `bash -lc "command -v dabs"`).Output()
		if err != nil {
			d.dabsErr = fmt.Errorf("ssh: cannot run dabs on %s (needs pubkey auth and dabs installed): %w", d.host, err)
			return
		}
		d.dabs = strings.TrimSpace(string(out))
		if d.dabs == "" {
			d.dabsErr = fmt.Errorf("ssh: dabs not found on %s", d.host)
		}
	})
	return d.dabs, d.dabsErr
}

// quote makes s safe as a single word through the remote shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// remote builds the ssh invocation for one remote command line.
func (d *Driver) remote(extraSSH []string, argv ...string) *exec.Cmd {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, quote(a))
	}
	args := append([]string{"-o", "BatchMode=yes"}, extraSSH...)
	args = append(args, d.host, strings.Join(quoted, " "))
	return exec.Command("ssh", args...)
}

func (d *Driver) stagingDir(name string) string { return ".dabs/staging/" + name }

// Build ships the build inputs to the remote staging dir and builds there.
func (d *Driver) Build(spec sandbox.BuildSpec) error {
	stage := d.stagingDir(spec.Name)

	reset := d.remote(nil, "rm", "-rf", stage)
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: reset staging on %s: %v: %s", d.host, err, strings.TrimSpace(string(out)))
	}
	if out, err := d.remote(nil, "mkdir", "-p", stage+"/context").CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: staging on %s: %v: %s", d.host, err, strings.TrimSpace(string(out)))
	}

	// Ship the Dockerfile (it may live outside the context).
	if err := runQuiet(exec.Command("scp", "-q", "-o", "BatchMode=yes", spec.Dockerfile, d.host+":"+stage+"/Dockerfile.dabs")); err != nil {
		return fmt.Errorf("ssh: ship Dockerfile to %s: %w", d.host, err)
	}

	// Ship the context as a tar stream, like docker ships context to its daemon.
	tar := exec.Command("tar", "-C", spec.Context, "-cf", "-", ".")
	untar := d.remote(nil, "tar", "-xf", "-", "-C", stage+"/context")
	pipe, err := tar.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	untar.Stdin = pipe
	untar.Stderr = os.Stderr
	if err := tar.Start(); err != nil {
		return fmt.Errorf("ssh: tar context: %w", err)
	}
	if err := untar.Run(); err != nil {
		return fmt.Errorf("ssh: ship context to %s: %w", d.host, err)
	}
	if err := tar.Wait(); err != nil {
		return fmt.Errorf("ssh: tar context: %w", err)
	}

	if err := d.writeRecipe(spec.Name, nil, ""); err != nil {
		return err
	}

	dabs, err := d.dabsPath()
	if err != nil {
		return err
	}
	build := d.remote(nil, dabs, "build", stage)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("ssh: dabs build on %s: %w", d.host, err)
	}
	return nil
}

// writeRecipe (re)writes the staged dabs.yaml recipe. Build writes it without
// runtime fields; Up rewrites it with the spec's workdir/env. It is emitted as
// JSON — valid YAML, which the remote dabs parses as a recipe — with the
// staging layout's Dockerfile.dabs + context/ as the recipe's inline image (the
// remote resolves those relative to the staged dabs.yaml's directory).
func (d *Driver) writeRecipe(name string, env map[string]string, workdir string) error {
	rec := map[string]any{"image": map[string]any{"dockerfile": "Dockerfile.dabs", "context": "context"}}
	if workdir != "" {
		rec["workdir"] = workdir
	}
	if len(env) > 0 {
		rec["env"] = env
	}
	reg := map[string]any{"default": name, "recipes": map[string]any{name: rec}}
	raw, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	w := d.remote(nil, "sh", "-c", "mkdir -p "+d.stagingDir(name)+" && cat > "+d.stagingDir(name)+"/dabs.yaml")
	w.Stdin = bytes.NewReader(raw)
	if out, err := w.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: write recipe on %s: %v: %s", d.host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Up rewrites the staged recipe with the spec's runtime fields and runs
// `dabs up` remotely, returning the instance name the remote printed.
func (d *Driver) Up(spec sandbox.Spec) (string, error) {
	if err := d.writeRecipe(spec.Name, spec.Env, spec.Workdir); err != nil {
		return "", err
	}
	dabs, err := d.dabsPath()
	if err != nil {
		return "", err
	}
	out, err := d.remote(nil, dabs, "up", d.stagingDir(spec.Name)).Output()
	if err != nil {
		return "", fmt.Errorf("ssh: dabs up on %s: %w", d.host, err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out))) // "<instance> up"
	if len(fields) < 1 {
		return "", fmt.Errorf("ssh: unexpected dabs up output on %s: %q", d.host, string(out))
	}
	return fields[0], nil
}

// Run executes remotely, streams wired through ssh. A TTY is requested when
// local stdin is a terminal so interactive use works end to end.
func (d *Driver) Run(instance string, cmd []string) error {
	dabs, err := d.dabsPath()
	if err != nil {
		return err
	}
	var extra []string
	if stdinIsTerminal() {
		extra = append(extra, "-t")
	}
	argv := append([]string{dabs, "run", instance, "--"}, cmd...)
	c := d.remote(extra, argv...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("ssh: run on %s: %w", d.host, err)
	}
	return nil
}

// Exec executes remotely and returns combined output.
func (d *Driver) Exec(instance string, cmd []string) (string, error) {
	dabs, err := d.dabsPath()
	if err != nil {
		return "", err
	}
	argv := append([]string{dabs, "run", instance, "--"}, cmd...)
	out, err := d.remote(nil, argv...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh: exec on %s: %v: %s", d.host, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Down removes the exactly-named remote instance (exact names match
// outright on the remote); absent is not an error there either.
func (d *Driver) Down(instance string) error {
	dabs, err := d.dabsPath()
	if err != nil {
		return err
	}
	if out, err := d.remote(nil, dabs, "down", instance).CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: down on %s: %v: %s", d.host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Ls lists the remote instances, passing the remote driver tag through
// untouched; WHICH target a row came from is fleet knowledge, printed by
// actions as its own column.
func (d *Driver) Ls() ([]sandbox.Info, error) {
	dabs, err := d.dabsPath()
	if err != nil {
		return nil, err
	}
	out, err := d.remote(nil, dabs, "ls").Output()
	if err != nil {
		return nil, fmt.Errorf("ssh: ls on %s: %w", d.host, err)
	}
	var infos []sandbox.Info
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 { // e.g. "(no dabs sandboxes)"
			continue
		}
		infos = append(infos, sandbox.Info{Name: parts[0], Status: parts[1], Driver: parts[2]})
	}
	return infos, nil
}

func runQuiet(c *exec.Cmd) error {
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HasImage does not cheaply probe the remote, so it reports false and lets the
// caller rebuild (safe and idempotent). Remote sandboxes are addressed by
// recipe target, never by the local-only build-skipping callers.
func (d *Driver) HasImage(string) (bool, error) { return false, nil }

// Kind identifies this driver by its transport.
func (d *Driver) Kind() string { return d.via }
