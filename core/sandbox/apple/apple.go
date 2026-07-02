//go:build darwin

// Package apple implements sandbox.Driver on Apple's `container` CLI — each
// sandbox is a lightweight Linux micro-VM (macOS 26+, Apple Silicon).
// Each sandbox is a long-lived `sleep infinity` container; the image is the
// clean state, so pristine reset = recreate from image.
package apple

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

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

func containerName(name string) string { return prefix + name }

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

// Up ensures the sandbox container is running. fresh removes it first so the
// image's clean state is restored.
func (d Driver) Up(spec sandbox.Spec, fresh bool) error {
	ctn := containerName(spec.Name)
	if !fresh {
		if state, err := d.state(ctn); err == nil && state == "running" {
			return nil
		}
	}
	remove(ctn) // fresh, stopped, or leftover — all collide on the name
	args := []string{"run", "-d", "--name", ctn, "-w", spec.Workdir}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, imageName(spec.Name), "sleep", "infinity")
	if out, err := exec.Command("container", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("apple: container run %s: %v: %s", ctn, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Down removes the sandbox container; absent is not an error.
func (Driver) Down(name string) error {
	remove(containerName(name))
	return nil
}

// Ls lists the containers dabs manages (running or not).
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
		infos = append(infos, sandbox.Info{Name: strings.TrimPrefix(c.ID, prefix), Status: c.Status.State})
	}
	return infos, nil
}

// listedContainer is the slice of `container ls --format json` output dabs
// reads; the CLI has no name/template filters (json|table|yaml|toml only),
// so we parse JSON and filter ourselves.
type listedContainer struct {
	ID     string `json:"id"`
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

// state returns the container's state ("running", "stopped", …) or an error
// if it does not exist.
func (Driver) state(ctn string) (string, error) {
	ctns, err := list()
	if err != nil {
		return "", err
	}
	for _, c := range ctns {
		if c.ID == ctn {
			return c.Status.State, nil
		}
	}
	return "", fmt.Errorf("apple: %s not found", ctn)
}

// remove force-removes a container, tolerating absence.
func remove(ctn string) {
	_ = exec.Command("container", "rm", "-f", ctn).Run()
}
