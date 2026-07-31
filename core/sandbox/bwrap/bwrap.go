//go:build linux

// Package bwrap implements sandbox.Driver on bubblewrap + overlayfs for
// millisecond starts. An instance is NOT a process: it is a directory (an
// overlay upper layer over an exported image rootfs) plus metadata. Up is a
// mkdir; every Run/Exec enters the overlay with bwrap (~ms); Down is rm -rf.
// Pristine = fresh upper layer — the image is the clean state.
//
// docker is used as the BUILDER only (Dockerfile → exported rootfs); it
// never runs instances. Isolation is user-namespace level (config
// isolation, not a security boundary): shared kernel, and processes do not
// outlive their Run call — every process an enter starts is bound to the one
// above it by --die-with-parent, and the outermost is bound to dabs by a
// parent-death signal. The network is a namespace of the box's own under
// every egress mode: none and proxy unshare it and leave it bare (proxy
// reaches the host proxy through a mounted unix socket, which is filesystem,
// not network), and open unshares it and hands it to pasta, which carries
// outbound traffic as userspace socket translation.
package bwrap

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/clidriver"
	"github.com/jjmerino/dabs/core/sandbox/execx"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
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
	// Egress and ProxySock replay the Spec's egress on every enter — the box
	// has no long-lived process, so the network decision is re-applied per
	// Run/Exec. A meta.json without these fields decodes to open.
	Egress       string `json:"egress,omitempty"`
	ProxySock    string `json:"proxySock,omitempty"`
	ForwarderBin string `json:"forwarderBin,omitempty"` // host path of the forwarder binary to mount
}

// Build runs `docker build` on the manifest's Dockerfile, then exports the
// image's flattened rootfs to the image dir, replacing any previous build.
func (d Driver) Build(spec sandbox.BuildSpec) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("bwrap: 'docker' not found (dabs builds images with it); install: https://docs.docker.com/engine/install/ (%w)", sandbox.ErrNoBuilder)
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
	// Refuse at boot for the two conditions that will not change between now and
	// the first command — a root caller, and no pasta on PATH — so a box that can
	// never have the namespace its egress names does not come up looking healthy.
	// What that pasta can do is the invocation's business, and Up's own probe: a
	// boot enters the box once with a no-op before reporting success.
	if openEgress(spec.Egress) {
		if _, err := requirePasta(); err != nil {
			return "", err
		}
	}
	instance, err := clidriver.InstanceName(spec.Name)
	if err != nil {
		return "", fmt.Errorf("bwrap: %w", err)
	}
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
	meta := instanceMeta{Workdir: spec.Workdir, Env: env, Mounts: spec.Mounts, Egress: spec.Egress, ProxySock: spec.ProxySock, ForwarderBin: spec.ForwarderBin}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return "", err
	}
	return instance, nil
}

// openEgress reports whether a mode means unrestricted outbound. The empty
// string is what a Spec carries when no egress was asked for, and open is the
// default.
func openEgress(mode string) bool { return mode == "" || mode == sandbox.EgressOpen }

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
	open := openEgress(meta.Egress)
	if !open {
		// None and proxy both start from a box with no network — loopback only.
		// Proxy's way out is the host socket bound below: a unix socket crosses
		// the netns because it is filesystem, not network.
		args = append(args, "--unshare-net")
	} else {
		if err := d.writeResolvConf(instance); err != nil {
			return nil, err
		}
		// docker export does not carry the runtime-injected resolv.conf, so the
		// box's copy is empty and DNS (package installs, downloads) would fail.
		// The box's namespace resolves at pasta, so it is pasta's address the
		// box's resolv.conf names. A restricted box has no resolver to reach —
		// names resolve at the proxy (HTTP CONNECT), or not at all.
		args = append(args, "--ro-bind", d.resolvConfPath(instance), "/etc/resolv.conf")
	}
	// Live host mounts: writes pass through to the host and outlive the box.
	for _, m := range meta.Mounts {
		bind := "--bind"
		if m.RO {
			bind = "--ro-bind"
		}
		args = append(args, bind, m.Host, m.Path)
	}
	if meta.Egress == sandbox.EgressProxy {
		// The host proxy's socket and the single-purpose forwarder binary land
		// read-only at the forwarder's fixed paths — bound AFTER the recipe
		// mounts, since bwrap binds in argv order and a recipe source at /run
		// listed later would silently mask them. The forwarder is a static linux
		// binary dabs materialized on the host — the caller's supplied one, else
		// its embedded copy.
		args = append(args,
			"--ro-bind", meta.ProxySock, forwarder.SockPath,
			"--ro-bind", meta.ForwarderBin, forwarder.ForwardPath)
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
	// The box has no long-lived process, so under proxy egress the forwarder
	// brackets each entered command: it binds the loopback listener, then runs
	// the command as its child for exactly as long as the command lives.
	if meta.Egress == sandbox.EgressProxy && len(cmd) > 0 {
		cmd = forwarder.WrapCommand(cmd)
	}
	// End bwrap's own option parsing before the box command. Without this a
	// command whose first token starts with '-' (e.g. `exec box -- --version`)
	// is parsed as a bwrap flag instead of run in the box.
	if len(cmd) > 0 {
		args = append(args, "--")
	}
	args = append(args, cmd...)
	if !open {
		return exec.Command("bwrap", args...), nil
	}
	// Open egress: pasta makes the namespace and runs bwrap inside it, as its
	// own child, so the namespace lives exactly as long as the entered command.
	// bwrap's --die-with-parent binds bwrap to pasta, and Pdeathsig asks the
	// kernel to SIGKILL pasta when dabs dies. What that guards against is a dabs
	// that is killed rather than returning: pasta and its bwrap would keep the
	// instance's overlay mounted while the flock they were covered by is already
	// free, so the next enter stacks a second overlay on the same upper dir and
	// rm reaps under a live writer. When dabs does return normally it has waited
	// on pasta, and the chain is gone with it. The lock already serializes
	// entries, so one entered command is one namespace.
	//
	// The kernel raises Pdeathsig when the THREAD that started pasta dies, not
	// when dabs does, so the caller runs the command on a pinned thread
	// (runOnLockedThread).
	pasta, err := requirePasta()
	if err != nil {
		return nil, err
	}
	c := exec.Command(pasta, pastaArgs(append([]string{"bwrap"}, args...))...)
	c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return c, nil
}

// runOnLockedThread runs f with the goroutine pinned to its OS thread, for as
// long as the entered command lives. A command carrying Pdeathsig is killed
// when the thread that forked it exits, and Go moves goroutines between threads
// and retires idle ones freely; pinning is what makes the signal mean "dabs is
// gone" rather than "the scheduler moved on".
func runOnLockedThread(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return f()
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
	if err := runOnLockedThread(c.Run); err != nil {
		return execx.BoxErr(fmt.Sprintf("bwrap: run in %s", instance), nil, err)
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
	var out []byte
	err = runOnLockedThread(func() error {
		var cerr error
		out, cerr = c.CombinedOutput()
		return cerr
	})
	if err != nil {
		return string(out), execx.BoxErr(fmt.Sprintf("bwrap: exec in %s", instance), out, err)
	}
	return string(out), nil
}

// CheckDetach reports that this driver cannot hold a detached command, and why.
// An instance is an overlay directory, not a process: every Run/Exec enters it
// with a FRESH bwrap that owns the sandbox for exactly as long as the command
// runs (its own PID namespace, an exclusive lock for the duration, and a death
// bond to the process that started it — --die-with-parent to whatever entered
// the box, and a parent-death signal from there to dabs). A command therefore
// cannot outlive the call that started it, and nothing else could enter the box
// while it did.
func (d Driver) CheckDetach() error {
	return fmt.Errorf("the bwrap driver cannot hold a detached command: it enters the box with a fresh bwrap per command, so a command cannot outlive the call that started it")
}

// Detach refuses for the reason CheckDetach gives. The capability is implemented
// so the reason lives with the driver that owns it rather than being guessed by
// a caller.
func (d Driver) Detach(instance string, cmd []string) error { return d.CheckDetach() }

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

// CheckEgress reports nil for the two modes a caller asks about before booting.
// None is one namespace flag; proxy additionally mounts a forwarder binary into
// the box, which dabs materializes from its embedded copy — the linux host makes
// it directly runnable in the box. bwrap needs nothing of the host for either,
// so it can always enforce both. Open is the mode with a host dependency here
// (pasta, and an unprivileged caller); nothing asks about open, and Up refuses
// it with pasta's own conditions named.
func (Driver) CheckEgress(mode string) error { return nil }

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
