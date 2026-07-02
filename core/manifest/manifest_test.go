package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, DefaultFilename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"name":"demo"}`)

	for _, arg := range []string{dir, filepath.Join(dir, DefaultFilename)} {
		m, err := Load(arg)
		if err != nil {
			t.Fatalf("Load(%s): %v", arg, err)
		}
		if m.Name != "demo" || m.Workdir != "/work" || m.Dir != dir {
			t.Errorf("Load(%s) = %+v, want Name=demo Workdir=/work Dir=%s", arg, m, dir)
		}
	}
}

func TestLoadReadsFields(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"name":"demo","workdir":"/app","env":{"K":"v"}}`)

	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Workdir != "/app" || m.Env["K"] != "v" {
		t.Errorf("got %+v, want Workdir=/app Env[K]=v", m)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Error("Load(empty dir) = nil error, want missing-manifest error")
	}
	write(t, dir, `{"workdir":"/app"}`)
	if _, err := Load(dir); err == nil {
		t.Error("Load(no name) = nil error, want missing-name error")
	}
}
