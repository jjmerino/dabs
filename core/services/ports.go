package services

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// The host serves every published service from one reserved band of loopback
// ports. A port is an opaque handle: it carries no meaning beyond leading to a
// service, and a name keeps the same one across boxes so a URL a human wrote
// down still works after the box it pointed at was re-upped.
const (
	PortRangeLow  = 42000
	PortRangeHigh = 42999
	// IndexPort serves the list of services and their addresses.
	IndexPort = 28080
)

// Ports is the persisted name → host port assignment. It lives in a small JSON
// file so an assignment outlives the process that made it. Every method takes
// the lock: within one process the scan loop and the index handler reach it from
// different goroutines, and the file is written by rename so another process
// reading it never sees half a store.
type Ports struct {
	path string

	mu     sync.Mutex
	byName map[string]int
}

// portsFile is the on-disk shape of the store.
type portsFile struct {
	Ports map[string]int `json:"ports"`
}

// LoadPorts reads the assignment store at path, treating an absent file as an
// empty store.
func LoadPorts(path string) (*Ports, error) {
	p := &Ports{path: path, byName: map[string]int{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var f portsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("service ports %s: %w", path, err)
	}
	for name, port := range f.Ports {
		p.byName[name] = port
	}
	return p, nil
}

// Port returns the port already assigned to name, and whether there is one.
func (p *Ports) Port(name string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	port, ok := p.byName[name]
	return port, ok
}

// Assign returns the host port for name, minting and persisting one on first
// sight. free reports whether a candidate port can be taken; the caller passes
// the real probe, and a test passes a canned answer.
func (p *Ports) Assign(name string, free func(int) bool) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if port, ok := p.byName[name]; ok {
		return port, nil
	}
	return p.mint(name, free)
}

// Reassign gives name a DIFFERENT port and persists it. It is what an
// assignment that can no longer be bound gets: the port is held by something
// else now, and a name nobody can reach is worth less than a name whose address
// changed.
func (p *Ports) Reassign(name string, free func(int) bool) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.byName, name)
	return p.mint(name, free)
}

// mint picks the lowest free port in the range and records it. The lock is
// already held.
func (p *Ports) mint(name string, free func(int) bool) (int, error) {
	taken := map[int]bool{}
	for _, port := range p.byName {
		taken[port] = true
	}
	for port := PortRangeLow; port <= PortRangeHigh; port++ {
		if taken[port] || !free(port) {
			continue
		}
		p.byName[name] = port
		if err := p.save(); err != nil {
			return 0, err
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port left in %d-%d for service %q", PortRangeLow, PortRangeHigh, name)
}

// save writes the store, creating its directory. It writes a temp file and
// renames it over the store, so a reader never sees a half-written file — this
// store is read by other dabs processes while a serving one writes it.
func (p *Ports) save() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(p.byName))
	for name := range p.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	f := portsFile{Ports: map[string]int{}}
	for _, name := range names {
		f.Ports[name] = p.byName[name]
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".ports-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p.path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// FreeOnLoopback reports whether port can be bound on 127.0.0.1 right now —
// the probe Assign uses on a real host, so an assignment never lands on a port
// something else already holds.
func FreeOnLoopback(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
