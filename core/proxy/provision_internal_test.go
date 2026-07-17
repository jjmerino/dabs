package proxy

// buildChain must hand the engine an ABSOLUTE module path: the engine imports
// from its own temp dir, so a relative path (as a project ./dabs.yaml selected by
// name yields) would fail the engine's import even though it passed the cwd-
// relative existence check. Regression for that mismatch.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jjmerino/dabs/core/recipe"
)

func TestBuildChainAbsolutizesRelativeModule(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)
	if err := os.WriteFile("hook.ts", []byte("export default () => ({})"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := func(s string) (string, error) { return s, nil }

	chain, err := buildChain("r", []recipe.ProxyHop{{Module: "hook.ts"}}, identity)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("chain len = %d, want 1", len(chain))
	}
	if !filepath.IsAbs(chain[0].Module) {
		t.Errorf("module path not absolutized: %q", chain[0].Module)
	}
	if _, err := os.Stat(chain[0].Module); err != nil {
		t.Errorf("absolutized module path does not resolve: %v", err)
	}
	if filepath.Base(chain[0].Module) != "hook.ts" {
		t.Errorf("module basename = %q, want hook.ts", filepath.Base(chain[0].Module))
	}
}
