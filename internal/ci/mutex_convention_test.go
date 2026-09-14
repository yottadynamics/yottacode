package ci

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestProductionMutexesUseSyncutil keeps deadlock-tag coverage from silently
// shrinking when new locks are added. syncutil itself is the only package that
// may name the standard mutex types because it owns the build-tagged aliases.
func TestProductionMutexesUseSyncutil(t *testing.T) {
	root := filepath.Join("..", "..")
	var violations []string
	for _, tree := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path == filepath.Join(root, "internal", "syncutil") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || (selector.Sel.Name != "Mutex" && selector.Sel.Name != "RWMutex") {
					return true
				}
				ident, ok := selector.X.(*ast.Ident)
				if ok && ident.Name == "sync" {
					pos := fset.Position(selector.Pos())
					rel := strings.TrimPrefix(path, root+string(filepath.Separator))
					violations = append(violations, rel+":"+fmt.Sprint(pos.Line)+": direct sync."+selector.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", tree, err)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production mutexes must use internal/syncutil so -tags deadlock instruments them:\n%s", strings.Join(violations, "\n"))
	}
}
