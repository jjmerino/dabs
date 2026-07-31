package services

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
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
// file so an assignment outlives the process that made it.
type Ports struct {
	path   string
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
	port, ok := p.byName[name]
	return port, ok
}

// Assign returns the host port for name, minting and persisting one on first
// sight. free reports whether a candidate port can be taken; the caller passes
// the real probe, and a test passes a canned answer.
func (p *Ports) Assign(name string, free func(int) bool) (int, error) {
	if port, ok := p.byName[name]; ok {
		return port, nil
	}
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

// save writes the store, creating its directory.
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
	return os.WriteFile(p.path, append(b, '\n'), 0o644)
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
