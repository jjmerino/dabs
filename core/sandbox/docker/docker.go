// Package docker implements sandbox.Driver on the `docker` CLI — each instance
// is a long-lived privileged container born from the image. Privileged so an
// image that starts its own dockerd (docker-in-docker) works, which is what
// lets a dabs box run docker-dependent workloads. PROTOTYPE quality.
package docker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/mattn/go-isatty"
)

const prefix = "dabs-"

// Driver runs boxes as docker containers. When nested is set (the INTERNAL
// privileged variant), Up adds the elevation a nested bwrap driver needs:
// --privileged (create user namespaces) + a non-overlay volume for dabs state
// (else the overlay mount stacks on docker's overlayfs → EINVAL). No docker
// runs inside the box — this is only for running another SANDBOX inside it.
type Driver struct{ nested bool }

// New returns the plain (unprivileged) driver, or an error if `docker` is absent.
func New() (Driver, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return Driver{}, fmt.Errorf("docker: 'docker' not found; install: https://docs.docker.com/engine/install/")
	}
	return Driver{}, nil
}

// NewNested is the INTERNAL privileged variant, for running a nested sandbox.
func NewNested() (Driver, error) {
	d, err := New()
	d.nested = true
	return d, err
}

func containerName(instance string) string { return prefix + instance }
func imageName(name string) string         { return prefix + name }

func (Driver) Build(spec sandbox.BuildSpec) error {
	cmd := exec.Command("docker", "build", "-t", imageName(spec.Name), "-f", spec.Dockerfile, spec.Context)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker: docker build %s: %w", imageName(spec.Name), err)
	}
	return nil
}

func (d Driver) Up(spec sandbox.Spec) (string, error) {
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("docker: generating instance id: %w", err)
	}
	instance := fmt.Sprintf("%s-%s", spec.Name, hex.EncodeToString(id))
	args := []string{"run", "-d", "--name", containerName(instance), "-w", spec.Workdir}
	if d.nested {
		args = append(args, "--privileged", "-v", "/tmp")
	}
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
	// sleep infinity keeps the box alive; docker exec inherits the container's
	// env and image WORKDIR, so Run/Exec need not re-pass them.
	args = append(args, imageName(spec.Name), "sleep", "infinity")
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker: docker run %s: %v: %s", containerName(instance), err, strings.TrimSpace(string(out)))
	}
	return instance, nil
}

func (Driver) exists(instance string) bool {
	return exec.Command("docker", "inspect", containerName(instance)).Run() == nil
}

// execFlags picks the `docker exec` flags for an interactive Run: always -i,
// plus -t when stdin is a real terminal so an interactive shell attaches.
func execFlags(tty bool) []string {
	if tty {
		return []string{"exec", "-i", "-t"}
	}
	return []string{"exec", "-i"}
}

func (d Driver) Run(instance string, cmd []string) error {
	if !d.exists(instance) {
		return fmt.Errorf("docker: no instance %q (see dabs ls)", instance)
	}
	// -i keeps stdin attached (piped input, agents, scripts all rely on it).
	// Add -t only when the caller's stdin is a real terminal: `docker exec -t`
	// refuses a non-TTY stdin, so allocating a pseudo-TTY unconditionally would
	// break every non-interactive run. With a TTY, -t gives a real shell.
	args := execFlags(isatty.IsTerminal(os.Stdin.Fd()))
	args = append(args, containerName(instance))
	args = append(args, cmd...)
	c := exec.Command("docker", args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker: exec in %s: %w", instance, err)
	}
	return nil
}

func (d Driver) Exec(instance string, cmd []string) (string, error) {
	if !d.exists(instance) {
		return "", fmt.Errorf("docker: no instance %q (see dabs ls)", instance)
	}
	args := append([]string{"exec", containerName(instance)}, cmd...)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker: exec in %s: %v: %s", instance, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (Driver) Down(instance string) error {
	_ = exec.Command("docker", "rm", "-f", containerName(instance)).Run()
	return nil
}

func (Driver) Ls() ([]sandbox.Info, error) {
	out, err := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}\t{{.State}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker: docker ps: %w", err)
	}
	var infos []sandbox.Info
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		name, state, _ := strings.Cut(line, "\t")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		infos = append(infos, sandbox.Info{Name: strings.TrimPrefix(name, prefix), Status: state, Driver: "docker"})
	}
	return infos, nil
}

// HasImage reports whether name's image is already built.
func (Driver) HasImage(name string) (bool, error) {
	err := exec.Command("docker", "image", "inspect", imageName(name)).Run()
	return err == nil, nil
}

func (Driver) Kind() string { return "docker" }

// Images lists the images dabs built under docker — the tags carrying dabs's
// prefix, reported under their recipe image name. Size is left 0 (docker's
// listing reports a human string, not bytes); a prune reaps by name.
func (Driver) Images() ([]sandbox.Image, error) {
	out, err := exec.Command("docker", "images", "--format", "{{.Repository}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker: images: %w", err)
	}
	seen := map[string]bool{}
	var imgs []sandbox.Image
	for _, repo := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasPrefix(repo, prefix) || seen[repo] {
			continue
		}
		seen[repo] = true
		imgs = append(imgs, sandbox.Image{Name: strings.TrimPrefix(repo, prefix)})
	}
	return imgs, nil
}

// RemoveImage deletes one dabs image tag. A missing image is not an error;
// an image still used by a container is (docker refuses it) so the caller can
// report it rather than reap silently.
func (Driver) RemoveImage(name string) error {
	out, err := exec.Command("docker", "rmi", imageName(name)).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such image") {
			return nil
		}
		return fmt.Errorf("docker: remove image %s: %s", imageName(name), strings.TrimSpace(string(out)))
	}
	return nil
}
