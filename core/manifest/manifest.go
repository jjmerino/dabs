// Package manifest loads a dabs sandbox manifest (dabs.json) from a path or
// a directory containing one.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
)

// DefaultFilename is the manifest filename looked up when given a directory.
const DefaultFilename = "dabs.json"

// friendlyType turns a Go type from a json decode error into plain words, so a
// bad manifest says "must be an object" instead of leaking map[string]string.
func friendlyType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Map, reflect.Struct:
		return "an object"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true or false"
	default:
		return t.String()
	}
}

// Manifest describes a program's sandbox.
type Manifest struct {
	Name       string            `json:"name"`    // required; sandbox identity
	Workdir    string            `json:"workdir"` // default /work
	Env        map[string]string `json:"env"`
	Dockerfile string            `json:"dockerfile"` // build recipe, relative to Dir; default Dockerfile
	Context    string            `json:"context"`    // build context, relative to Dir; default .
	Target     string            `json:"target"`     // which fleet driver runs the box (e.g. "docker", a server name); default local

	// Dir is the directory containing the manifest file (context for
	// relative paths). Set by Load, not part of the JSON.
	Dir string `json:"-"`
}

// Load reads the manifest at pathOrDir, which may be the manifest file itself
// or a directory containing DefaultFilename. Defaults are applied.
func Load(pathOrDir string) (Manifest, error) {
	var m Manifest
	path := pathOrDir
	if st, err := os.Stat(pathOrDir); err == nil && st.IsDir() {
		path = filepath.Join(pathOrDir, DefaultFilename)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, fmt.Errorf("no manifest at %q — build/up take a manifest file or a directory containing a dabs.json (to run a recipe, use: dabs recipe <name>)", pathOrDir)
	}
	if err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			if te.Field == "" { // a top-level type mismatch has no field name
				return m, fmt.Errorf("manifest %s: must be %s (got %s)", path, friendlyType(te.Type), te.Value)
			}
			return m, fmt.Errorf("manifest %s: field %q must be %s (got %s)", path, te.Field, friendlyType(te.Type), te.Value)
		}
		return m, fmt.Errorf("manifest %s: invalid JSON: %w", path, err)
	}
	if m.Name == "" {
		return m, fmt.Errorf("manifest %s: missing required 'name'", path)
	}
	if m.Workdir == "" {
		m.Workdir = "/work"
	}
	if m.Dockerfile == "" {
		m.Dockerfile = "Dockerfile"
	}
	if m.Context == "" {
		m.Context = "."
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return m, fmt.Errorf("manifest %s: %w", path, err)
	}
	m.Dir = abs
	return m, nil
}
