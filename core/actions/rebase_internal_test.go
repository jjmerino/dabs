package actions

// A dabs.yaml loaded BY PATH anchors its relative proxy `module:` paths on its
// OWN directory, exactly as it does for source origins and its image — so a
// module resolves alongside the yaml that declared it, from any cwd. Absolute,
// `~`, and `$VAR` module paths are left for later expansion; a tls hop (empty
// module) is untouched. For a project ./dabs.yaml (auto-loaded from the cwd) the
// declaring dir IS the cwd, so this rebasing is a no-op there — it only bites the
// path form, where the yaml lives elsewhere.

import (
	"testing"

	"github.com/jjmerino/dabs/core/recipe"
)

func TestRebaseAnchorsProxyModulePathsOnYamlDir(t *testing.T) {
	reg := recipe.Registry{Recipes: map[string]recipe.Recipe{
		"gate": {
			Egress: recipe.Egress{Mode: recipe.EgressProxy, HTTPProxy: []recipe.ProxyHop{
				{Module: "gate.ts"},       // relative → anchored on the yaml dir
				{Module: "hooks/swap.ts"}, // relative subdir → anchored too
				{TLS: "terminate"},        // a tls hop has no module → untouched
				{Module: "/abs/keep.ts"},  // absolute → left alone
				{Module: "$HOME/env.ts"},  // $VAR → left for expansion
			}},
		},
	}}

	rebaseSourcePaths(&reg, "/proj/box")

	got := reg.Recipes["gate"].Egress.HTTPProxy
	want := []string{"/proj/box/gate.ts", "/proj/box/hooks/swap.ts", "", "/abs/keep.ts", "$HOME/env.ts"}
	for i, w := range want {
		if got[i].Module != w {
			t.Errorf("hop %d module = %q, want %q", i, got[i].Module, w)
		}
	}
	// The tls hop must stay a tls hop after rebasing.
	if got[2].TLS != "terminate" {
		t.Errorf("tls hop mangled: %+v", got[2])
	}
}
