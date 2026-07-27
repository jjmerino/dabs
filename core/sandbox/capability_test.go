package sandbox

// A wrapper that forgets to forward an optional capability does not fail — it
// answers "this driver cannot", naming a driver that can. The compile-time
// `var _ Capable` lines catch that, but only for capabilities someone remembered
// to put in Capable. This test closes the remaining gap by reading the package's
// own source: every interface declared an OPTIONAL driver capability must be in
// Capable, so the next one added is pinned to every wrapper whether or not its
// author knew wrappers exist.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// optionalCapabilityMark is the phrase a capability's doc comment carries. It is
// the same wording every one of them already opens with.
const optionalCapabilityMark = "OPTIONAL driver capability"

// declaredOptionalCapabilities parses this package and returns, per interface
// declared an optional capability, its method names.
func declaredOptionalCapabilities(t *testing.T) map[string][]string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the sandbox package: %v", err)
	}
	found := map[string][]string{}
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		ast.Inspect(pkg, func(n ast.Node) bool {
			decl, ok := n.(*ast.GenDecl)
			if !ok || decl.Tok != token.TYPE {
				return true
			}
			for _, spec := range decl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				// The doc sits on the GenDecl for a lone type, on the spec inside
				// a parenthesized block.
				doc := ts.Doc
				if doc == nil {
					doc = decl.Doc
				}
				if doc == nil || !strings.Contains(doc.Text(), optionalCapabilityMark) {
					continue
				}
				var methods []string
				for _, m := range iface.Methods.List {
					for _, id := range m.Names {
						methods = append(methods, id.Name)
					}
				}
				found[ts.Name.Name] = methods
			}
			return true
		})
	}
	if len(found) == 0 {
		t.Fatal("no optional capabilities found; the marker or the parse is wrong, and this test would pass vacuously")
	}
	return found
}

// CONTRACT: every optional capability is listed in Capable. Capable is what
// wrappers are pinned to, so a capability missing from it is a capability every
// wrapper is free to drop.
func TestCapableListsEveryOptionalCapability(t *testing.T) {
	capable := reflect.TypeOf((*Capable)(nil)).Elem()
	for name, methods := range declaredOptionalCapabilities(t) {
		for _, m := range methods {
			if _, ok := capable.MethodByName(m); !ok {
				t.Errorf("capability %s is not in Capable (missing %s); every wrapper may silently drop it", name, m)
			}
		}
	}
}

// CONTRACT: every wrapper in the driver path answers for every optional
// capability. The compile-time assertions beside each wrapper say so; this names
// them in one place so a new wrapper without one is visible as an omission here.
func TestWrappersSatisfyCapable(t *testing.T) {
	wrappers := map[string]any{
		"lazy": (*lazy)(nil),
	}
	capable := reflect.TypeOf((*Capable)(nil)).Elem()
	for name, w := range wrappers {
		if got := reflect.TypeOf(w); !got.Implements(capable) {
			t.Errorf("wrapper %s does not implement Capable; it drops an optional capability", name)
		}
	}
}
