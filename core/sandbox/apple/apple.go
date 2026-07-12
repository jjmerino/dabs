//go:build darwin

// Package apple implements sandbox.Driver on Apple's `container` CLI — each
// sandbox instance is a lightweight Linux micro-VM (macOS 26+, Apple
// Silicon). Every Up creates a new long-lived `sleep infinity` container
// born pristine from the image.
package apple

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"github.com/jjmerino/dabs/core/sandbox"
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
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("apple: generating instance id: %w", err)
	}
	instance := fmt.Sprintf("%s-%s", spec.Name, hex.EncodeToString(id))
	args := []string{"run", "-d", "--name", containerName(instance), "-w", spec.Workdir}
	// DABS_NAME marks the box: anything running inside can detect it is
	// sandboxed.
	args = append(args, "-e", "DABS_NAME="+instance)
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	// Live host mounts: writes pass through to the host and outlive the box.
	for _, m := range spec.Mounts {
		mount := fmt.Sprintf("type=bind,source=%s,target=%s", m.Host, m.Path)
		if m.RO {
			mount += ",readonly"
		}
		args = append(args, "--mount", mount)
	}
	args = append(args, imageName(spec.Name), "sleep", "infinity")
	if out, err := exec.Command("container", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("apple: container run %s: %v: %s", containerName(instance), err, strings.TrimSpace(string(out)))
	}
	return instance, nil
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
	interactive := stdinIsTerminal()
	if interactive {
		args = append(args, "-i", "-t")
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
	if interactive {
		c.Stdin = os.Stdin
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		// A non-zero EXIT is the box command's own failure, not dabs's: return it
		// bare so the caller propagates the code and prints no dabs-error line.
		// Anything else (the vendor CLI could not spawn the exec) is a driver failure.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return fmt.Errorf("apple: exec in %s: %w", ctn, err)
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
		// A non-zero EXIT is the box command's own failure: return it bare so the
		// caller propagates the code. Only a real driver failure is wrapped.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee
		}
		return string(out), fmt.Errorf("apple: exec in %s: %v: %s", found.ID, err, strings.TrimSpace(string(out)))
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

// remove force-removes a container, tolerating absence.
func remove(ctn string) {
	_ = exec.Command("container", "rm", "-f", ctn).Run()
}

// Kind identifies this driver.
func (Driver) Kind() string { return "apple" }

// Images lists the images dabs built under this driver — the `container` image
// tags carrying dabs's prefix, reported under their recipe image name. Size is
// left 0: `container` does not report it in the listing, and a prune reaps by
// name regardless.
func (Driver) Images() ([]sandbox.Image, error) {
	out, err := exec.Command("container", "image", "ls").Output()
	if err != nil {
		return nil, fmt.Errorf("apple: image ls: %w", err)
	}
	seen := map[string]bool{}
	var imgs []sandbox.Image
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.Fields(line)
		if len(name) == 0 || !strings.HasPrefix(name[0], prefix) || seen[name[0]] {
			continue
		}
		seen[name[0]] = true
		imgs = append(imgs, sandbox.Image{Name: strings.TrimPrefix(name[0], prefix)})
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
