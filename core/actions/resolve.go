package actions

import (
	"fmt"
	"strings"
)

// matches resolves a possibly-abbreviated instance name against what the
// driver reports — git-style: an exact match wins outright (a full name is
// never shadowed by being a prefix of another), otherwise every instance
// the prefix matches. This is dabs domain logic: drivers only see exact
// names.
func (r Real) matches(instance string) ([]string, error) {
	infos, err := r.driver.Ls()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, in := range infos {
		if in.Name == instance {
			return []string{in.Name}, nil
		}
		if strings.HasPrefix(in.Name, instance) {
			out = append(out, in.Name)
		}
	}
	return out, nil
}

// resolveOne is matches for verbs that need exactly one target (run, mcp).
func (r Real) resolveOne(instance string) (string, error) {
	m, err := r.matches(instance)
	if err != nil {
		return "", err
	}
	if len(m) == 0 {
		return "", fmt.Errorf("no instance matches %q (see dabs ls)", instance)
	}
	if len(m) > 1 {
		return "", fmt.Errorf("%q is ambiguous: %s (see dabs ls)", instance, strings.Join(m, ", "))
	}
	return m[0], nil
}
