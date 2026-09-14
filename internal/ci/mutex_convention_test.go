package ci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestMutexesUseSyncutil keeps deadlock-tag coverage from silently shrinking
// when production or test locks are added. syncutil is the only package that
// may import the standard sync package for Mutex/RWMutex declarations because
// it owns both the build-tagged aliases and the documented polling exception.
func TestMutexesUseSyncutil(t *testing.T) {
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
			if !strings.HasSuffix(path, ".go") {
				return nil
			}

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			aliases := syncImportAliases(file)
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || (selector.Sel.Name != "Mutex" && selector.Sel.Name != "RWMutex") {
					return true
				}
				ident, ok := selector.X.(*ast.Ident)
				if ok && aliases[ident.Name] {
					violations = append(violations, mutexViolation(path, root, fset.Position(selector.Pos()).Line, "sync."+selector.Sel.Name))
				}
				return true
			})
			if aliases["."] {
				ast.Inspect(file, func(node ast.Node) bool {
					ident, ok := node.(*ast.Ident)
					if !ok || (ident.Name != "Mutex" && ident.Name != "RWMutex") || ident.Obj != nil {
						return true
					}
					violations = append(violations, mutexViolation(path, root, fset.Position(ident.Pos()).Line, "dot-imported sync."+ident.Name))
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", tree, err)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("mutexes must use internal/syncutil so -tags deadlock instruments them:\n%s", strings.Join(violations, "\n"))
	}
}

// syncImportAliases returns every local name bound to the standard sync
// package. Import paths, rather than identifier spelling, define the match.
func syncImportAliases(file *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "sync" {
			continue
		}
		name := "sync"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" {
			aliases[name] = true
		}
	}
	return aliases
}

// mutexViolation formats a stable repository-relative diagnostic.
func mutexViolation(path, root string, line int, kind string) string {
	rel := strings.TrimPrefix(path, root+string(filepath.Separator))
	return rel + ":" + strconv.Itoa(line) + ": direct " + kind
}
