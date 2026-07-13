package actions

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// warnf is where resolution warnings go: stderr, NEVER stdout, so a warning
// never mingles with a command's real output on stdout.
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
// Fast path: every LOCAL driver (any non-server driver — apple, docker) is
// queried FIRST, synchronously, and an exact match among them returns before
// any remote is contacted, so addressing a local box by its full name never
// pays an ssh round-trip or risks hanging on a slow server. Only when no local
// driver holds an exact match are the remote servers queried — concurrently,
// each bounded by remoteTimeout.
func (r Real) matches(instance string) ([]match, error) {
	// A name is REQUIRED: an empty/blank name is a prefix of EVERY instance, so
	// without this it would "match" the whole fleet (reported as ambiguous, one
	// `if` away from acting on all of them). Blank matches nothing, never all —
	// for every verb that resolves a name, not just down.
	if strings.TrimSpace(instance) == "" {
		return nil, fmt.Errorf("a name is required (see dabs ls)")
	}
	var out []match

	// Local drivers first, synchronously and in fleet order — an exact match on
	// any of them short-circuits the whole fleet, so a docker box resolves
	// without ever contacting a server.
	for _, key := range r.order {
		drv := r.drivers[key]
		if isServer(drv.Kind()) {
			continue
		}
		infos, err := lsTimeout(drv, remoteTimeout)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		for _, in := range infos {
			if in.Name == instance {
				return []match{{name: in.Name, target: key, driver: drv}}, nil
			}
			if strings.HasPrefix(in.Name, instance) {
				out = append(out, match{name: in.Name, target: key, driver: drv})
			}
		}
	}

	// No local exact match — query the remote servers concurrently; a timed-out/
	// failed server warns and is skipped rather than failing or hanging the whole
	// resolution.
	type reply struct {
		key   string
		infos []sandbox.Info
	}
	ch := make(chan reply, len(r.order))
	remotes := 0
	for _, key := range r.order {
		if !isServer(r.drivers[key].Kind()) {
			continue
		}
		remotes++
		go func(key string, drv sandbox.Driver) {
			infos, err := lsTimeout(drv, remoteTimeout)
			if err != nil {
				fmt.Fprintln(warnf, tui.Warn("dabs: server %q unreachable, skipping: %v", key, err))
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
		if !isServer(r.drivers[key].Kind()) {
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

// resolveOne is matches for verbs that need exactly one target (exec, run).
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
