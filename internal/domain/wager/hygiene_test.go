package wager_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDomainHygiene keeps every domain package free of Fx, HTTP, SQS and
// persistence imports and of floating point types: only the standard
// library, google/uuid and other domain packages are allowed.
func TestDomainHygiene(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	const domain = "github.com/junglegaming/backend-challenge-go/internal/domain/"
	forbidden := []string{"net/http", "database/sql"}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(path, "/")
			external := strings.Contains(first, ".")
			if external && path != "github.com/google/uuid" && !strings.HasPrefix(path, domain) {
				t.Errorf("%s imports %q", name, path)
			}
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %q", name, path)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
				t.Errorf("%s uses %s at %s", name, id.Name, fset.Position(id.Pos()))
			}
			return true
		})
	}
}
