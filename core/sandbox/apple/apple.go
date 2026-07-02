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
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, imageName(spec.Name), "sleep", "infinity")
	if out, err := exec.Command("container", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("apple: container run %s: %v: %s", containerName(instance), err, strings.TrimSpace(string(out)))
	}
	return instance, nil
}

// resolve finds the dabs instance whose name starts with instancePrefix —
// git-style: any unambiguous prefix addresses the instance. Exact matches
// win outright (so a full name can't be shadowed by being a prefix of
// another). Returns the matched containers (0, 1, or more).
func resolve(instancePrefix string, ctns []listedContainer) []listedContainer {
	want := containerName(instancePrefix)
	var matches []listedContainer
	for _, c := range ctns {
		if c.ID == want {
			return []listedContainer{c}
		}
		if strings.HasPrefix(c.ID, want) {
			matches = append(matches, c)
		}
	}
	return matches
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
	matches := resolve(instance, ctns)
	if len(matches) == 0 {
		return fmt.Errorf("apple: no instance matching %q (see dabs ls)", instance)
	}
	if len(matches) > 1 {
		return fmt.Errorf("apple: %q is ambiguous: %s (see dabs ls)", instance, strings.Join(instanceNames(matches), ", "))
	}
	found := &matches[0]
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
	matches := resolve(instance, ctns)
	if len(matches) == 0 {
		return "", fmt.Errorf("apple: no instance matching %q (see dabs ls)", instance)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("apple: %q is ambiguous: %s (see dabs ls)", instance, strings.Join(instanceNames(matches), ", "))
	}
	found := &matches[0]
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
		return string(out), fmt.Errorf("apple: exec in %s: %v: %s", found.ID, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Down removes instances matching the (possibly abbreviated) name; absent
// is not an error. On multiple matches it removes them all only under
// force; otherwise it removes nothing and reports the matches — never
// guess which box to destroy.
func (Driver) Down(instance string, force bool) ([]string, error) {
	ctns, err := list()
	if err != nil {
		return nil, err
	}
	matches := resolve(instance, ctns)
	if len(matches) > 1 && !force {
		return nil, sandbox.AmbiguousError{Instance: instance, Matches: instanceNames(matches)}
	}
	removed := make([]string, 0, len(matches))
	for _, c := range matches {
		remove(c.ID)
		removed = append(removed, strings.TrimPrefix(c.ID, prefix))
	}
	return removed, nil
}

// instanceNames renders container ids as user-facing instance names.
func instanceNames(ctns []listedContainer) []string {
	out := make([]string, 0, len(ctns))
	for _, c := range ctns {
		out = append(out, strings.TrimPrefix(c.ID, prefix))
	}
	return out
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
