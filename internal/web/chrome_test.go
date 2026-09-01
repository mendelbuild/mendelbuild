package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Chrome is the part of every page that belongs to the layout rather than to
// any one handler: the signed-in user, the current nav section, and the count
// of open requests.
//
// This is a source-level test, which needs justifying. The failure it exists to
// catch is not a wrong value but a missing call: twice now, the line that
// stamps chrome has been silently dropped — once by handlers each having their
// own copy and five of them forgetting, and once by an edit that removed the
// central one. Neither showed up as a test failure, because a page renders
// perfectly well without a nav badge, and the deployed build simply had no
// account menu.
//
// A behavioural test cannot reach it: two of the three stamps need a database,
// and this package has none in test. So this asserts the shape of the code
// instead, which is exactly the property that keeps breaking.
func TestRenderPageForStampsChrome(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "handlers.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing handlers.go: %v", err)
	}

	bodies := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		start := fset.Position(fn.Body.Pos()).Offset
		end := fset.Position(fn.Body.End()).Offset
		bodies[fn.Name.Name] = string(source(t)[start:end])
		return true
	})

	render, ok := bodies["renderPageFor"]
	if !ok {
		t.Fatal("renderPageFor is gone; every page render must go through one place")
	}
	if !strings.Contains(render, "addChrome") {
		t.Error("renderPageFor no longer stamps chrome: pages will render with no " +
			"signed-in user, no active nav section, and no open-request badge")
	}

	chrome, ok := bodies["addChrome"]
	if !ok {
		t.Fatal("addChrome is gone")
	}
	for _, want := range []string{"addUserToData", `data["Nav"]`, "addOpenInputCount"} {
		if !strings.Contains(chrome, want) {
			t.Errorf("addChrome no longer does %s", want)
		}
	}

	// The point of one central stamp is that there are no others. A handler
	// adding its own is the arrangement that lost the user in the first place.
	for name, body := range bodies {
		if name == "addChrome" || name == "renderPageFor" {
			continue
		}
		if strings.Contains(body, "addOpenInputCount") || strings.Contains(body, "addUserToData") {
			t.Errorf("%s stamps chrome itself; it belongs in addChrome so it cannot be forgotten", name)
		}
	}
}

func source(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("reading handlers.go: %v", err)
	}
	return b
}
