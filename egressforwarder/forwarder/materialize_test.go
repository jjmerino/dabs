package forwarder

// Materializing the binary a proxy box will mount. A program embedding dabs as a
// module never has the embedded copy — forward.bin is generated at build time
// and ships in neither the repo nor the module zip — so supplying one is the
// only door it has, and these are that door's contract.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CONTRACT: a supplied forwarder is materialized VERBATIM and executable. The
// contract is the forwarder protocol, not a particular build, so a caller's own
// binary — here bytes that are nothing like the embedded copy — reaches the box
// byte for byte, with no comparison or version check standing in the way.
func TestMaterializeUsesTheSuppliedBinary(t *testing.T) {
	src := filepath.Join(t.TempDir(), "my-forwarder")
	want := []byte("a forwarder speaking the protocol, plus extras\x00\x01\x02")
	if err := os.WriteFile(src, want, 0o755); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path, err := Materialize(dir, src)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if path != filepath.Join(dir, "forward") {
		t.Errorf("materialized at %q, want %q", path, filepath.Join(dir, "forward"))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("materialized bytes = %q, want the supplied binary %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("materialized forwarder is not executable: mode %v", info.Mode())
	}
}

// CONTRACT: a supplied path that is not there, or is not a file, is refused —
// and the error names the path. The alternative is a box that boots with
// nothing (or a directory) mounted where its only way out should be.
func TestMaterializeRefusesAnUnusableSuppliedPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	notAFile := t.TempDir()

	for _, c := range []struct{ name, supplied, want string }{
		{"missing", missing, missing},
		{"a directory", notAFile, "not a regular file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := t.TempDir()
			path, err := Materialize(out, c.supplied)
			if err == nil {
				t.Fatalf("want a refusal, got %q", path)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name %q", err, c.want)
			}
			if !strings.Contains(err.Error(), "supplied forwarder") {
				t.Errorf("error %q does not say the SUPPLIED forwarder is at fault", err)
			}
			if _, err := os.Stat(filepath.Join(out, "forward")); err == nil {
				t.Error("a refused supply still wrote a forwarder")
			}
		})
	}
}
