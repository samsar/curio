package layout

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParams_NameEveryConstant: Params names every constant of the package
// but the three it leaves out on purpose, so a constant added or changed
// later can't change what the views draw while a prior map recorded
// without it still starts the next one warm or is reused.
func TestParams_NameEveryConstant(t *testing.T) {
	leftOut := map[string]bool{
		"goldenAngle":    true, // a mathematical constant
		"latticeSpacing": true, // latticeGap's
		"roundingSlack":  true, // checks a view, draws nothing
	}
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	declared, named := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		require.NoError(t, err)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				if d.Tok != token.CONST {
					continue
				}
				for _, spec := range d.Specs {
					for _, id := range spec.(*ast.ValueSpec).Names {
						declared[id.Name] = true
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil || d.Name.Name != "Params" {
					continue
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok {
						named[id.Name] = true
					}
					return true
				})
			}
		}
	}
	require.NotEmpty(t, named, "Params is in the package")
	for _, c := range slices.Sorted(maps.Keys(declared)) {
		assert.True(t, named[c] != leftOut[c], "constant %s: in Params %v, left out on purpose %v", c, named[c], leftOut[c])
	}
}
