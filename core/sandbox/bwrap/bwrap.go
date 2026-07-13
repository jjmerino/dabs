//go:build linux

// Package bwrap implements sandbox.Driver on bubblewrap + overlayfs for
// millisecond starts. An instance is NOT a process: it is a directory (an
// overlay upper layer over an exported image rootfs) plus metadata. Up is a
// mkdir; every Run/Exec enters the overlay with bwrap (~ms); Down is rm -rf.
// Pristine = fresh upper layer — the image is the clean state.
//
// docker is used as the BUILDER only (Dockerfile → exported rootfs); it
// never runs instances. Isolation is user-namespace level (config
// isolation, not a security boundary): shared kernel, shared network, and
// processes do not outlive their Run call.
package bwrap

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jjmerino/dabs/core/sandbox"
)

// Driver stores images and instances under root (~/.dabs).
type Driver struct {
	root string
}

// New returns the driver, or an error if bwrap is missing. bwrap enters
// instances (up/run/down/ls); docker is needed ONLY to build images and is
// checked in Build, not here — so a host that only runs prebuilt images
// (builds happen elsewhere) needs no docker.
func New() (Driver, error) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return Driver{}, fmt.Errorf("bwrap: 'bwrap' not found; install: apt install bubblewrap (or your distro's bubblewrap package)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Driver{}, fmt.Errorf("bwrap: %w", err)
	}
	return Driver{root: filepath.Join(home, ".dabs")}, nil
}

func (d Driver) imageDir(name string) string { return filepath.Join(d.root, "images", name) }
func (d Driver) instanceDir(instance string) string {
	return filepath.Join(d.root, "instances", instance)
}

// imageMeta is what Build records about the image; instanceMeta is what Up
// records about an instance. Run/Exec read instanceMeta back — the instance
// carries its own truth, no manifest involved.
type imageMeta struct {
	Env     []string `json:"env"`     // the image's ENV (docker Config.Env)
	Workdir string   `json:"workdir"` // the image's WORKDIR
}

type instanceMeta struct {
	Workdir string          `json:"workdir"`
	Env     []string        `json:"env"`    // K=V, image env merged with spec env
	Mounts  []sandbox.Mount `json:"mounts"` // live host paths bound into the box
}

// Build runs `docker build` on the manifest's Dockerfile, then exports the
// image's flattened rootfs to the image dir, replacing any previous build.
func (d Driver) Build(spec sandbox.BuildSpec) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("bwrap: 'docker' not found (dabs builds images with it); install: https://docs.docker.com/engine/install/")
	}
	tag := "dabs-" + spec.Name
	build := exec.Command("docker", "build", "-t", tag, "-f", spec.Dockerfile, spec.Context)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("bwrap: docker build %s: %w", tag, err)
	}

	// Flatten the image into a rootfs dir via a throwaway container.
	cidRaw, err := exec.Command("docker", "create", tag).Output()
	if err != nil {
		return fmt.Errorf("bwrap: docker create %s: %w", tag, err)
	}
	cid := strings.TrimSpace(string(cidRaw))
	defer exec.Command("docker", "rm", cid).Run()

	tmp := d.imageDir(spec.Name) + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	rootfs := filepath.Join(tmp, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	export := exec.Command("docker", "export", cid)
	untar := exec.Command("tar", "-x", "--exclude=dev/*", "-C", rootfs)
	untar.Stdin, err = export.StdoutPipe()
	if err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	untar.Stderr = os.Stderr
	if err := export.Start(); err != nil {
		return fmt.Errorf("bwrap: docker export: %w", err)
	}
	if err := untar.Run(); err != nil {
		return fmt.Errorf("bwrap: untar rootfs: %w", err)
	}
	if err := export.Wait(); err != nil {
		return fmt.Errorf("bwrap: docker export: %w", err)
	}

	// Record the image's env/workdir so instances inherit them.
	insRaw, err := exec.Command("docker", "image", "inspect", "--format",
		`{"env":{{json .Config.Env}},"workdir":{{json .Config.WorkingDir}}}`, tag).Output()
	if err != nil {
		return fmt.Errorf("bwrap: docker image inspect %s: %w", tag, err)
	}
	var im imageMeta
	if err := json.Unmarshal(insRaw, &im); err != nil {
		return fmt.Errorf("bwrap: image inspect output: %w", err)
	}
	if err := writeJSON(filepath.Join(tmp, "image.json"), im); err != nil {
		return err
	}

	dir := d.imageDir(spec.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	return nil
}

// Up creates a new pristine instance: an empty upper layer plus metadata.
// Nothing runs — entering happens per Run/Exec call.
func (d Driver) Up(spec sandbox.Spec) (string, error) {
	var im imageMeta
	if err := readJSON(filepath.Join(d.imageDir(spec.Name), "image.json"), &im); err != nil {
		return "", fmt.Errorf("bwrap: no image for %q (run dabs build first): %w", spec.Name, err)
	}
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("bwrap: generating instance id: %w", err)
	}
	instance := fmt.Sprintf("%s-%s", spec.Name, hex.EncodeToString(id))
	dir := d.instanceDir(instance)
	for _, sub := range []string{"upper", "work"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "", fmt.Errorf("bwrap: %w", err)
		}
	}
	env := mergeEnv(im.Env, spec.Env)
	// DABS_NAME marks the box: anything running inside can detect it is
	// sandboxed.
	env = append(env, "DABS_NAME="+instance)
	meta := instanceMeta{Workdir: spec.Workdir, Env: env, Mounts: spec.Mounts}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return "", err
	}
	return instance, nil
}

// enter builds the bwrap invocation for an instance. The overlay is mounted
// by bwrap itself (unprivileged, user namespace); writes land in the
// instance's upper layer and persist across calls.
func (d Driver) enter(instance string, cmd []string) (*exec.Cmd, error) {
	var meta instanceMeta
	if err := readJSON(filepath.Join(d.instanceDir(instance), "meta.json"), &meta); err != nil {
		return nil, fmt.Errorf("bwrap: no instance %q (see dabs ls): %w", instance, err)
	}
	args := []string{
		"--overlay-src", filepath.Join(d.imageDir(imageOf(instance)), "rootfs"),
		"--overlay", filepath.Join(d.instanceDir(instance), "upper"), filepath.Join(d.instanceDir(instance), "work"), "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-user", "--uid", "0", "--gid", "0", // look like root, as in the container the image was built for
		"--unshare-pid", // /proc shows only the box's processes, not the host's
		"--unshare-uts", "--hostname", instance,
		"--die-with-parent",
		"--chdir", meta.Workdir,
		"--clearenv",
	}
	// docker export does not carry the runtime-injected resolv.conf, so the
	// box's copy is empty and DNS (package installs, downloads) would fail.
	// The network is shared with the host anyway; share its DNS config too.
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}
	// Live host mounts: writes pass through to the host and outlive the box.
	for _, m := range meta.Mounts {
		bind := "--bind"
		if m.RO {
			bind = "--ro-bind"
		}
		args = append(args, bind, m.Host, m.Path)
	}
	haveHome := false
	for _, kv := range meta.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == "HOME" {
			haveHome = true
		}
		args = append(args, "--setenv", k, v)
	}
	// Images often don't declare ENV HOME; docker defaults it to /root for
	// the root user. Without it, `~` expands to nothing and per-box home
	// state (the point of a fresh machine) lands in /.
	if !haveHome {
		args = append(args, "--setenv", "HOME", "/root")
	}
	// End bwrap's own option parsing before the box command. Without this a
	// command whose first token starts with '-' (e.g. `exec box -- --version`)
	// is parsed as a bwrap flag instead of run in the box.
	if len(cmd) > 0 {
		args = append(args, "--")
	}
	args = append(args, cmd...)
	return exec.Command("bwrap", args...), nil
}

// imageOf recovers the sandbox name from an instance name (<name>-<hex12>).
func imageOf(instance string) string {
	if i := strings.LastIndex(instance, "-"); i > 0 {
		return instance[:i]
	}
	return instance
}

// lock serializes entries into one instance: concurrent overlayfs mounts
// sharing an upper dir are unsupported by the kernel (observed corrupting
// writes). Callers hold the flock for the duration of the command.
func (d Driver) lock(instance string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(d.instanceDir(instance), "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("bwrap: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("bwrap: locking %s: %w", instance, err)
	}
	return func() { f.Close() }, nil // closing releases the flock
}

// Run executes cmd inside the instance, streams wired to the caller. bwrap
// inherits stdio directly, so interactive use needs no TTY plumbing.
func (d Driver) Run(instance string, cmd []string) error {
	c, err := d.enter(instance, cmd)
	if err != nil {
		return err
	}
	unlock, err := d.lock(instance)
	if err != nil {
		return err
	}
	defer unlock()
	// Forward the host stdin so a pipe or heredoc into `dabs exec`
	// reaches the box command. With no piped input os.Stdin is a terminal or
	// /dev/null, both of which yield EOF, so a non-interactive command still
	// exits instead of hanging.
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		// A non-zero EXIT is the box command's own failure, not dabs's: return it
		// bare so the caller propagates the code and prints no dabs-error line.
		// Everything else (bwrap could not spawn, no rootfs) is a driver failure.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return fmt.Errorf("bwrap: run in %s: %w", instance, err)
	}
	return nil
}

// Exec runs cmd inside the instance non-interactively and returns combined
// output.
func (d Driver) Exec(instance string, cmd []string) (string, error) {
	c, err := d.enter(instance, cmd)
	if err != nil {
		return "", err
	}
	unlock, err := d.lock(instance)
	if err != nil {
		return "", err
	}
	defer unlock()
	out, err := c.CombinedOutput()
	if err != nil {
		// A non-zero EXIT is the box command's own failure: return it bare so the
		// caller propagates the code. Only a real driver failure is wrapped.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee
		}
		return string(out), fmt.Errorf("bwrap: exec in %s: %v: %s", instance, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Down removes the exactly-named instance; absent is not an error.
func (d Driver) Down(instance string) error {
	if err := os.RemoveAll(d.instanceDir(instance)); err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	return nil
}

// Ls lists instances. There is no daemon and nothing runs between calls, so
// every instance is simply "ready".
func (d Driver) Ls() ([]sandbox.Info, error) {
	entries, err := os.ReadDir(filepath.Join(d.root, "instances"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bwrap: %w", err)
	}
	var infos []sandbox.Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		infos = append(infos, sandbox.Info{Name: e.Name(), Status: "ready", Driver: "bwrap"})
	}
	return infos, nil
}

// mergeEnv layers spec env over image env (spec wins by key).
func mergeEnv(imageEnv []string, specEnv map[string]string) []string {
	out := make([]string, 0, len(imageEnv)+len(specEnv))
	for _, kv := range imageEnv {
		k, _, _ := strings.Cut(kv, "=")
		if _, overridden := specEnv[k]; !overridden {
			out = append(out, kv)
		}
	}
	for k, v := range specEnv {
		out = append(out, k+"="+v)
	}
	return out
}

func writeJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("bwrap: %w", err)
	}
	return nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// HasImage reports whether name's image (a staged rootfs dir) already exists.
func (d Driver) HasImage(name string) (bool, error) {
	_, err := os.Stat(d.imageDir(name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Kind identifies this driver.
func (Driver) Kind() string { return "bwrap" }

// Images lists the built image rootfs trees under <root>/images. Each is a
// directory a Build produced; its size is the whole tree, since that is what a
// prune reclaims.
func (d Driver) Images() ([]sandbox.Image, error) {
	entries, err := os.ReadDir(filepath.Join(d.root, "images"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bwrap: images: %w", err)
	}
	var out []sandbox.Image
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, sandbox.Image{Name: e.Name(), Size: dirSize(filepath.Join(d.root, "images", e.Name()))})
	}
	return out, nil
}

// RemoveImage deletes one built image tree. A missing image is not an error.
func (d Driver) RemoveImage(name string) error {
	if err := os.RemoveAll(d.imageDir(name)); err != nil {
		return fmt.Errorf("bwrap: remove image %s: %w", name, err)
	}
	return nil
}

// dirSize sums the bytes of a tree, best-effort (an unreadable entry counts 0).
func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
