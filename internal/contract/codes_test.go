package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// declaredCodes parses contract.go and returns every non-empty Code constant in
// declaration order, so the doc table is checked against the source itself.
func declaredCodes(t *testing.T) []Code {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "contract.go", nil, 0)
	if err != nil {
		t.Fatalf("parse contract.go: %v", err)
	}
	var out []Code
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || spec.Type == nil {
			return true
		}
		if ident, ok := spec.Type.(*ast.Ident); !ok || ident.Name != "Code" {
			return true
		}
		for _, value := range spec.Values {
			lit, ok := value.(*ast.BasicLit)
			if !ok {
				continue
			}
			code, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", lit.Value, err)
			}
			if code != "" {
				out = append(out, Code(code))
			}
		}
		return true
	})
	return out
}

// The error reference an agent reads is rendered from CodeDocs, so a code added
// to contract.go without a row here would be invisible to agents.
func TestCodeDocsCoverEveryCode(t *testing.T) {
	declared := declaredCodes(t)
	if len(declared) == 0 {
		t.Fatal("found no Code constants in contract.go")
	}
	if len(CodeDocs) != len(declared) {
		t.Errorf("CodeDocs has %d rows, contract.go declares %d codes", len(CodeDocs), len(declared))
	}
	for i, code := range declared {
		if i >= len(CodeDocs) {
			t.Errorf("CodeDocs has no row for %s", code)
			continue
		}
		doc := CodeDocs[i]
		if doc.Code != code {
			t.Errorf("CodeDocs[%d] is %s, want %s (keep the declaration order)", i, doc.Code, code)
		}
		if doc.Exit == "" || doc.Meaning == "" || doc.Next == "" {
			t.Errorf("CodeDocs row for %s has an empty column", code)
		}
	}
}
