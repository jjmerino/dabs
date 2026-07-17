// Package docker implements sandbox.Driver on the `docker` CLI — each instance
// is a long-lived privileged container born from the image. Privileged so an
// image that starts its own dockerd (docker-in-docker) works, which is what
// lets a dabs box run docker-dependent workloads. PROTOTYPE quality.
package docker

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/sandbox/clidriver"
	"github.com/jjmerino/dabs/core/sandbox/execx"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
	"github.com/mattn/go-isatty"
)

const prefix = "dabs-"

// command builds a docker subprocess. A unit test swaps it to run a stand-in
// so the error-surfacing policy can be exercised without a docker daemon.
var command = exec.Command

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
	cmd := command("docker", "build", "-t", imageName(spec.Name), "-f", spec.Dockerfile, spec.Context)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker: docker build %s: %w", imageName(spec.Name), err)
	}
	return nil
}

func (d Driver) Up(spec sandbox.Spec) (string, error) {
	instance, err := clidriver.InstanceName(spec.Name)
	if err != nil {
		return "", fmt.Errorf("docker: %w", err)
	}
	args := []string{"run", "-d", "--name", containerName(instance), "-w", spec.Workdir}
	if d.nested {
		args = append(args, "--privileged", "-v", "/tmp")
	}
	// None and proxy both start from a container with no network: proxy's only
	// way out is the host socket mounted below, which is filesystem, not network.
	if spec.Egress == sandbox.EgressNone || spec.Egress == sandbox.EgressProxy {
		args = append(args, "--network", "none")
	}
	args = append(args, "-e", "DABS_NAME="+instance)
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	// Live host mounts: writes pass through to the host and outlive the box.
	for _, m := range spec.Mounts {
		args = append(args, "--mount", clidriver.MountArg(m))
	}
	// sleep infinity keeps the box alive; docker exec inherits the container's
	// env and image WORKDIR, so Run/Exec need not re-pass them.
	keepAlive := []string{"sleep", "infinity"}
	if spec.Egress == sandbox.EgressProxy {
		// The host proxy's socket and the single-purpose forwarder binary land
		// read-only at the forwarder's fixed paths, and the forwarder brackets
		// the keep-alive so it serves 127.0.0.1 for the container's whole life.
		// The forwarder is a static linux binary dabs materialized from its
		// embedded copy. The socket is a FILE mount, capturing its inode at run
		// time: a proxy that recreates its socket orphans this container's mount,
		// and the box needs a re-up.
		args = append(args,
			"--mount", clidriver.MountArg(sandbox.Mount{Host: spec.ProxySock, Path: forwarder.SockPath, RO: true}),
			"--mount", clidriver.MountArg(sandbox.Mount{Host: spec.ForwarderBin, Path: forwarder.ForwardPath, RO: true}))
		keepAlive = forwarder.WrapCommand(keepAlive)
	}
	args = append(args, imageName(spec.Name))
	args = append(args, keepAlive...)
	if out, err := command("docker", args...).CombinedOutput(); err != nil {
		return "", execx.WrapOut(fmt.Sprintf("docker: docker run %s", containerName(instance)), out, err)
	}
	return instance, nil
}

func (Driver) exists(instance string) bool {
	return command("docker", "inspect", containerName(instance)).Run() == nil
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
	c := command("docker", args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return execx.BoxErr(fmt.Sprintf("docker: exec in %s", instance), nil, err)
	}
	return nil
}

func (d Driver) Exec(instance string, cmd []string) (string, error) {
	if !d.exists(instance) {
		return "", fmt.Errorf("docker: no instance %q (see dabs ls)", instance)
	}
	args := append([]string{"exec", containerName(instance)}, cmd...)
	out, err := command("docker", args...).CombinedOutput()
	if err != nil {
		return string(out), execx.BoxErr(fmt.Sprintf("docker: exec in %s", instance), out, err)
	}
	return string(out), nil
}

func (Driver) Down(instance string) error {
	_ = command("docker", "rm", "-f", containerName(instance)).Run()
	return nil
}

func (Driver) Ls() ([]sandbox.Info, error) {
	out, err := command("docker", "ps", "-a", "--format", "{{.Names}}\t{{.State}}").Output()
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
	err := command("docker", "image", "inspect", imageName(name)).Run()
	return err == nil, nil
}

func (Driver) Kind() string { return "docker" }

// CheckEgress: none is a stock docker flag; proxy mounts a forwarder binary and
// a host unix socket into the container. It needs a linux host — Docker
// Desktop's VM makes the cross-boundary socket FILE mount the proxy depends on
// unreliable. The forwarder itself is a linux binary dabs materialized from its
// embedded copy, so the binary is never the limiting factor.
func (Driver) CheckEgress(mode string) error {
	if mode != sandbox.EgressProxy {
		return nil
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("docker: proxy egress needs a linux host — Docker Desktop's VM makes the proxy's cross-boundary socket mount unreliable (got %s)", runtime.GOOS)
	}
	return nil
}

// Images lists the images dabs built under docker — the tags carrying dabs's
// prefix, reported under their recipe image name. Size is left 0 (docker's
// listing reports a human string, not bytes); a prune reaps by name.
func (Driver) Images() ([]sandbox.Image, error) {
	out, err := command("docker", "images", "--format", "{{.Repository}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker: images: %w", err)
	}
	repos := strings.Split(strings.TrimSpace(string(out)), "\n")
	var imgs []sandbox.Image
	for _, name := range clidriver.FilterPrefixed(repos) {
		imgs = append(imgs, sandbox.Image{Name: name})
	}
	return imgs, nil
}

// RemoveImage deletes one dabs image tag. A missing image is not an error;
// an image still used by a container is (docker refuses it) so the caller can
// report it rather than reap silently.
func (Driver) RemoveImage(name string) error {
	out, err := command("docker", "rmi", imageName(name)).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such image") {
			return nil
		}
		return fmt.Errorf("docker: remove image %s: %s", imageName(name), strings.TrimSpace(string(out)))
	}
	return nil
}
