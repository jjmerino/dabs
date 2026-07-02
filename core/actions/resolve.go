package actions

import (
	"fmt"
	"strings"

	"github.com/jjmerino/dabs/core/sandbox"
)

// match is one instance found somewhere in the fleet.
type match struct {
	name   string // full instance name
	target string // fleet key ("local", or a config target name)
	driver sandbox.Driver
}

// matches resolves a possibly-abbreviated instance name against the WHOLE
// fleet — git-style: an exact match wins outright (a full name is never
// shadowed by being a prefix of another), otherwise every instance the
// prefix matches, on any target. This is dabs domain logic: drivers only
// see exact names.
func (r Real) matches(instance string) ([]match, error) {
	var out []match
	for _, key := range r.order {
		infos, err := r.drivers[key].Ls()
		if err != nil {
			return nil, err
		}
		for _, in := range infos {
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
