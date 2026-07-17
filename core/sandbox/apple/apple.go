//go:build darwin

// Package apple implements sandbox.Driver on Apple's `container` CLI — each
// sandbox instance is a lightweight Linux micro-VM (macOS 26+, Apple
// Silicon). Every Up creates a new long-lived `sleep infinity` container
// born pristine from the image.
package apple

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/clidriver"
	"github.com/jjmerino/dabs/core/sandbox/execx"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// prefix namespaces every container dabs manages.
const prefix = "dabs-"

// Driver shells out to the `container` CLI.
type Driver struct{}

// New returns the driver, or an error if the `container` CLI is not installed.
func New() (Driver, error) {
	if _, err := exec.LookPath("container"); err != nil {
		return Driver{}, fmt.Errorf("apple: 'container' CLI not found; install: brew install container && container system start")
	}
	return Driver{}, nil
}

func containerName(instance string) string { return prefix + instance }

// imageName is the local OCI tag a sandbox's image lives under. Image
// references are driver-owned: an OCI tag means nothing to a cloud provider,
// so this never appears in the sandbox contract.
func imageName(name string) string { return prefix + name }

// Build builds the sandbox's image with `container build`, streaming the
// vendor's build output to the user.
func (Driver) Build(spec sandbox.BuildSpec) error {
	cmd := exec.Command("container", "build", "-t", imageName(spec.Name), "-f", spec.Dockerfile, spec.Context)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apple: container build %s: %w", imageName(spec.Name), err)
	}
	return nil
}

// Up creates and starts a new pristine instance named <spec.Name>-<id>,
// id being random hex — unguessable, and addressable by unique prefix.
func (d Driver) Up(spec sandbox.Spec) (string, error) {
	instance, err := clidriver.InstanceName(spec.Name)
	if err != nil {
		return "", fmt.Errorf("apple: %w", err)
	}
	args := []string{"run", "-d", "--name", containerName(instance), "-w", spec.Workdir}
	// None and proxy both start from a micro-VM with no network — no routes,
	// no non-loopback interface. Proxy's only way out is the socket volume
	// below, which `container` relays across the VM boundary itself.
	if spec.Egress == sandbox.EgressNone || spec.Egress == sandbox.EgressProxy {
		args = append(args, "--network", "none")
	}
	// DABS_NAME marks the box: anything running inside can detect it is
	// sandboxed.
	args = append(args, "-e", "DABS_NAME="+instance)
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	// Live host mounts: writes pass through to the host and outlive the box.
	for _, m := range spec.Mounts {
		args = append(args, "--mount", clidriver.MountArg(m))
	}
	keepAlive := []string{"sleep", "infinity"}
	if spec.Egress == sandbox.EgressProxy {
		// A host binary cannot run in the linux micro-VM, so unlike the linux
		// drivers dabs does not mount the forwarder in — the IMAGE must carry a
		// linux forwarder at forwarder.ForwardPath, and the recipe's Dockerfile
		// is where it comes from. Probed before the real boot so a missing
		// forwarder is an instruction, not a dead box. The socket rides its own
		// --volume: `container` relays a socket FILE volume across the VM (the
		// --ssh mechanism); a socket inside a directory bind is dead virtiofs.
		if err := checkImageCarriesForwarder(spec.Name); err != nil {
			return "", err
		}
		args = append(args, "--volume", spec.ProxySock+":"+forwarder.SockPath)
		keepAlive = forwarder.WrapCommand(keepAlive)
	}
	args = append(args, imageName(spec.Name))
	args = append(args, keepAlive...)
	if out, err := exec.Command("container", args...).CombinedOutput(); err != nil {
		return "", execx.WrapOut(fmt.Sprintf("apple: container run %s", containerName(instance)), out, err)
	}
	return instance, nil
}

// checkImageCarriesForwarder boots a throwaway container to run the image's
// own forwarder binary with no arguments: present, it prints its usage;
// absent, the run fails — and the error hands the user the Dockerfile lines
// that fix it.
func checkImageCarriesForwarder(name string) error {
	out, err := exec.Command("container", "run", "--rm", imageName(name), forwarder.ForwardPath).CombinedOutput()
	if strings.Contains(string(out), "usage: forward ") {
		return nil
	}
	// The probe output is mostly the CLI's progress bars; the tail line names
	// the actual failure (a missing executable).
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	detail := lines[len(lines)-1]
	if err == nil {
		err = errors.New("unexpected probe output")
	}
	return fmt.Errorf("apple: proxy egress needs the image to carry a linux forwarder at %s (it bridges HTTP_PROXY to the proxy socket; a host binary cannot run in the micro-VM) — add to the image's Dockerfile:\n\n"+
		"  FROM golang:alpine AS fwd\n"+
		"  RUN CGO_ENABLED=0 go install github.com/jjmerino/dabs/egressforwarder/cmd/forward@latest\n\n"+
		"and in the final stage:\n\n"+
		"  COPY --from=fwd /go/bin/forward %s\n\n(probe: %v: %s)",
		forwarder.ForwardPath, forwarder.ForwardPath, err, detail)
}

// HasImage reports whether name's image is already built.
func (Driver) HasImage(name string) (bool, error) {
	err := exec.Command("container", "image", "inspect", imageName(name)).Run()
	return err == nil, nil
}

// find returns the container for an EXACT instance name, or nil.
func find(instance string, ctns []listedContainer) *listedContainer {
	want := containerName(instance)
	for i := range ctns {
		if ctns[i].ID == want {
			return &ctns[i]
		}
	}
	return nil
}

// Run executes cmd inside the named instance with the workdir and env the
// instance was created with (read back from the vendor, not a manifest),
// output streamed to this process. When stdin is a terminal it is attached
// with a TTY so interactive shells work; `container exec -i` on a
// non-terminal stdin fails with ENODEV, so batch runs get no stdin.
func (Driver) Run(instance string, cmd []string) error {
	ctns, err := list()
	if err != nil {
		return err
	}
	found := find(instance, ctns)
	if found == nil {
		return fmt.Errorf("apple: no instance %q (see dabs ls)", instance)
	}
	ctn := found.ID
	args := []string{"exec"}
	// `-i` attaches stdin so a pipe or heredoc reaches the box command, and `-t`
	// adds a pseudo-TTY for interactive use. The vendor `container exec -i`
	// rejects a stdin that is neither a terminal nor a real stream (/dev/null),
	// so pass `-i` only when there is one: a terminal or a piped file/pipe.
	tty := stdinIsTerminal()
	attach := tty || stdinIsStream()
	if attach {
		args = append(args, "-i")
	}
	if tty {
		args = append(args, "-t")
	}
	if wd := found.Configuration.InitProcess.WorkingDirectory; wd != "" {
		args = append(args, "-w", wd)
	}
	for _, kv := range found.Configuration.InitProcess.Environment {
		args = append(args, "-e", kv)
	}
	args = append(args, ctn)
	args = append(args, cmd...)
	c := exec.Command("container", args...)
	if attach {
		c.Stdin = os.Stdin
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return execx.BoxErr(fmt.Sprintf("apple: exec in %s", ctn), nil, err)
	}
	return nil
}

// Exec runs cmd inside the named instance non-interactively and returns its
// combined output. Like Run, workdir and env come from the instance itself.
func (Driver) Exec(instance string, cmd []string) (string, error) {
	ctns, err := list()
	if err != nil {
		return "", err
	}
	found := find(instance, ctns)
	if found == nil {
		return "", fmt.Errorf("apple: no instance %q (see dabs ls)", instance)
	}
	args := []string{"exec"}
	if wd := found.Configuration.InitProcess.WorkingDirectory; wd != "" {
		args = append(args, "-w", wd)
	}
	for _, kv := range found.Configuration.InitProcess.Environment {
		args = append(args, "-e", kv)
	}
	args = append(args, found.ID)
	args = append(args, cmd...)
	out, err := exec.Command("container", args...).CombinedOutput()
	if err != nil {
		return string(out), execx.BoxErr(fmt.Sprintf("apple: exec in %s", found.ID), out, err)
	}
	return string(out), nil
}

// Down removes the exactly-named instance; absent is not an error.
func (Driver) Down(instance string) error {
	remove(containerName(instance))
	return nil
}

// Ls lists the instances dabs manages (running or not).
func (d Driver) Ls() ([]sandbox.Info, error) {
	ctns, err := list()
	if err != nil {
		return nil, err
	}
	var infos []sandbox.Info
	for _, c := range ctns {
		if !strings.HasPrefix(c.ID, prefix) {
			continue
		}
		infos = append(infos, sandbox.Info{Name: strings.TrimPrefix(c.ID, prefix), Status: c.Status.State, Driver: "apple"})
	}
	return infos, nil
}

// listedContainer is the slice of `container ls --format json` output dabs
// reads; the CLI has no name/template filters (json|table|yaml|toml only),
// so we parse JSON and filter ourselves. InitProcess carries the workdir and
// env an instance was created with — Run reads them back from here.
type listedContainer struct {
	ID            string `json:"id"`
	Configuration struct {
		InitProcess struct {
			WorkingDirectory string   `json:"workingDirectory"`
			Environment      []string `json:"environment"`
		} `json:"initProcess"`
	} `json:"configuration"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

func list() ([]listedContainer, error) {
	out, err := exec.Command("container", "ls", "-a", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("apple: container ls: %w", err)
	}
	var ctns []listedContainer
	if err := json.Unmarshal(out, &ctns); err != nil {
		return nil, fmt.Errorf("apple: container ls output: %w", err)
	}
	return ctns, nil
}

// stdinIsTerminal reports whether stdin is a real TTY, via the terminal
// ioctl. A char-device check is NOT enough: /dev/null is a char device too.
// (darwin-only file, so raw syscall beats pulling in x/term.)
func stdinIsTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// stdinIsStream reports whether stdin is a pipe or a regular file — a real
// input source to forward. A char device (a terminal, or /dev/null when there
// is no input) is not a stream.
func stdinIsStream() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// remove force-removes a container, tolerating absence.
func remove(ctn string) {
	_ = exec.Command("container", "rm", "-f", ctn).Run()
}

// Kind identifies this driver.
func (Driver) Kind() string { return "apple" }

// CheckEgress reports nil for every mode: none and proxy both map to
// `container run --network none`, and proxy's extra requirement — the IMAGE must
// carry a linux forwarder — is an image question that Up
// probes for (an image question, not a mode question — the answer comes with
// Dockerfile instructions).
func (Driver) CheckEgress(mode string) error {
	return nil
}

// Images lists the images dabs built under this driver — the `container` image
// tags carrying dabs's prefix, reported under their recipe image name. Size is
// left 0: `container` does not report it in the listing, and a prune reaps by
// name regardless.
func (Driver) Images() ([]sandbox.Image, error) {
	out, err := exec.Command("container", "image", "ls").Output()
	if err != nil {
		return nil, fmt.Errorf("apple: image ls: %w", err)
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			tags = append(tags, f[0])
		}
	}
	var imgs []sandbox.Image
	for _, name := range clidriver.FilterPrefixed(tags) {
		imgs = append(imgs, sandbox.Image{Name: name})
	}
	return imgs, nil
}

// RemoveImage deletes one dabs image tag. A missing image is not an error.
func (Driver) RemoveImage(name string) error {
	if err := exec.Command("container", "image", "rm", imageName(name)).Run(); err != nil {
		if exec.Command("container", "image", "inspect", imageName(name)).Run() != nil {
			return nil // already gone
		}
		return fmt.Errorf("apple: remove image %s: %w", imageName(name), err)
	}
	return nil
}
