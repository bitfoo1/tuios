package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// errorfDirs are the trees whose packages the tuios binary links.
var errorfDirs = []string{"internal", "cmd/tuios", "pkg"}

// errorfSkip are folders inside errorfDirs that are not part of the binary,
// and errfmt itself, which wraps fmt.Errorf.
var errorfSkip = []string{"internal/errfmt", "internal/harness/herdrconv", "internal/lint"}

// TestErrorfGoesThroughErrfmt is the Errorf lint. Since Go 1.26 the compiler
// inlines fmt.Errorf, and the errors.New inside it, at every call site: about
// 115 bytes of binary per call, for about 1,800 calls. errfmt.Errorf is the
// same function kept out of line, so every call shares one copy. Moving the
// calls to it took 208 KB off the linux/amd64 binary.
//
// Rejected in the non-test files of the packages the binary links:
// fmt.Errorf, under any name fmt is imported as. A file built only by
// go run (//go:build ignore) is not part of the binary and may use it.
//
// It fails on an injected call; see TestErrorfLintCatchesACall.
func TestErrorfGoesThroughErrfmt(t *testing.T) {
	root := repoRoot(t)
	var found []string
	read := 0
	for _, dir := range errorfDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				for _, skip := range errorfSkip {
					if rel == skip {
						return filepath.SkipDir
					}
				}
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			read++
			found = append(found, errorfCalls(t, rel, src)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if read < 100 {
		t.Fatalf("the lint read %d files, so it reads the wrong folders", read)
	}
	if len(found) > 0 {
		t.Errorf("a call uses fmt.Errorf, which the compiler copies into every caller; "+
			"call errfmt.Errorf, which formats and wraps the same way:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// TestErrorfLintCatchesACall feeds the lint a file holding the form it
// rejects and the forms it allows, so a lint that quietly stopped matching
// cannot pass the check above by finding nothing.
func TestErrorfLintCatchesACall(t *testing.T) {
	src := `package x

import (
	"fmt"
	f2 "fmt"
)

func a() error { return fmt.Errorf("a %d", 1) }
func b() error { return f2.Errorf("b") }
func c() string { return fmt.Sprintf("c") }
func d() error { return errfmt.Errorf("d") }
`
	if got := errorfCalls(t, "x.go", []byte(src)); len(got) != 2 {
		t.Fatalf("the lint found %d calls, want 2 (a and b):\n%s", len(got), strings.Join(got, "\n"))
	}

	ignored := "//go:build ignore\n\npackage main\n\nimport \"fmt\"\n\nfunc main() { _ = fmt.Errorf(\"x\") }\n"
	if got := errorfCalls(t, "gen.go", []byte(ignored)); len(got) != 0 {
		t.Fatalf("the lint found %d calls in a go run file, want 0:\n%s", len(got), strings.Join(got, "\n"))
	}
}

// errorfCalls returns file:line for every fmt.Errorf in src.
func errorfCalls(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.TrimSpace(c.Text) == "//go:build ignore" {
				return nil
			}
		}
	}
	names := map[string]bool{}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "fmt" {
			local := "fmt"
			if imp.Name != nil {
				local = imp.Name.Name
			}
			names[local] = true
		}
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if pkg, sel := selector(asExpr(n)); names[pkg] && sel == "Errorf" {
			out = append(out, fmt.Sprintf("%s:%d: fmt.Errorf", name, fset.Position(n.Pos()).Line))
		}
		return true
	})
	return out
}
