// Package manifest loads a dabs sandbox manifest (dabs.json) from a path or
// a directory containing one.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultFilename is the manifest filename looked up when given a directory.
const DefaultFilename = "dabs.json"

// Manifest describes a program's sandbox.
type Manifest struct {
	Name       string            `json:"name"`    // required; sandbox identity
	Workdir    string            `json:"workdir"` // default /work
	Env        map[string]string `json:"env"`
	Dockerfile string            `json:"dockerfile"` // build recipe, relative to Dir; default Dockerfile
	Context    string            `json:"context"`    // build context, relative to Dir; default .

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
	if err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("manifest %s: %w", path, err)
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
