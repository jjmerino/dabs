package actions

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jjmerino/dabs/core/sandbox"
)

// warnf is where resolution warnings go: stderr, NEVER stdout. Resolution
// runs inside `dabs mcp`, whose stdout is the MCP protocol channel — a stray
// byte there corrupts the stream.
var warnf = os.Stderr

// remoteTimeout bounds how long a single remote driver's Ls may take during
// resolution. A slow or dead server degrades to "no matches from there"
// (with a warning) instead of hanging every command that resolves a name.
const remoteTimeout = 6 * time.Second

// match is one instance found somewhere in the fleet.
type match struct {
	name   string // full instance name
	target string // fleet key ("local", or a config target name)
	driver sandbox.Driver
}

// lsTimeout runs a driver's Ls bounded by a timeout. The goroutine may leak
// if the driver truly hangs (a wedged ssh), but the caller is freed.
func lsTimeout(d sandbox.Driver, timeout time.Duration) ([]sandbox.Info, error) {
	type res struct {
		infos []sandbox.Info
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		i, e := d.Ls()
		ch <- res{i, e}
	}()
	select {
	case r := <-ch:
		return r.infos, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
}

// matches resolves a possibly-abbreviated instance name against the fleet —
// git-style: an exact match wins outright, otherwise every instance the
// prefix matches, on any target. Domain logic: drivers only see exact names.
//
// Fast path: a LOCAL exact match returns before any remote is queried, so
// handing a local box to an agent (dabs mcp <full-name>) never pays an ssh
// round-trip or risks hanging on a slow server. Remote drivers are queried
// concurrently, each bounded by remoteTimeout.
func (r Real) matches(instance string) ([]match, error) {
	var out []match

	// Local first, synchronously — its exact match short-circuits the fleet.
	if drv, ok := r.drivers["local"]; ok {
		infos, err := lsTimeout(drv, remoteTimeout)
		if err != nil {
			return nil, fmt.Errorf("local: %w", err)
		}
		for _, in := range infos {
			if in.Name == instance {
				return []match{{name: in.Name, target: "local", driver: drv}}, nil
			}
			if strings.HasPrefix(in.Name, instance) {
				out = append(out, match{name: in.Name, target: "local", driver: drv})
			}
		}
	}

	// Remote servers concurrently; a timed-out/failed server warns and is
	// skipped rather than failing or hanging the whole resolution.
	type reply struct {
		key   string
		infos []sandbox.Info
	}
	ch := make(chan reply, len(r.order))
	remotes := 0
	for _, key := range r.order {
		if key == "local" {
			continue
		}
		remotes++
		go func(key string, drv sandbox.Driver) {
			infos, err := lsTimeout(drv, remoteTimeout)
			if err != nil {
				fmt.Fprintf(warnf, "dabs: warning: server %q unreachable, skipping: %v\n", key, err)
				ch <- reply{key, nil}
				return
			}
			ch <- reply{key, infos}
		}(key, r.drivers[key])
	}
	byKey := map[string][]sandbox.Info{}
	for i := 0; i < remotes; i++ {
		rep := <-ch
		byKey[rep.key] = rep.infos
	}

	// Process remotes in r.order for deterministic results; exact still wins.
	for _, key := range r.order {
		if key == "local" {
			continue
		}
		for _, in := range byKey[key] {
			if in.Name == instance {
				return []match{{name: in.Name, target: key, driver: r.drivers[key]}}, nil
			}
			if strings.HasPrefix(in.Name, instance) {
				out = append(out, match{name: in.Name, target: key, driver: r.drivers[key]})
			}
		}
	}
	return out, nil
}

// resolveOne is matches for verbs that need exactly one target (run, mcp).
func (r Real) resolveOne(instance string) (match, error) {
	m, err := r.matches(instance)
	if err != nil {
		return match{}, err
	}
	if len(m) == 0 {
		return match{}, fmt.Errorf("no instance matches %q (see dabs ls)", instance)
	}
	if len(m) > 1 {
		return match{}, fmt.Errorf("%q is ambiguous: %s (see dabs ls)", instance, names(m))
	}
	return m[0], nil
}

// names renders matches for user-facing messages.
func names(m []match) string {
	out := make([]string, 0, len(m))
	for _, x := range m {
		out = append(out, x.name)
	}
	return strings.Join(out, ", ")
}
