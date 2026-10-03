package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestEnumsWithCollidingGoNamesKeepBothWireValues(t *testing.T) {
	for name, definition := range map[string]*schema{
		"enum":  {Type: []byte(`"string"`), Enum: []any{"openai/form", "openaiForm"}},
		"union": {OneOf: []*schema{{Type: []byte(`"string"`), Enum: []any{"openai/form"}}, {Type: []byte(`"string"`), Enum: []any{"openaiForm"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			g := &generator{defs: map[string]*schema{"Mode": definition}}
			var b strings.Builder
			var inline []inlineType
			b.WriteString("package protocol\n")
			g.renderDef(&b, "Mode", definition, &inline, map[string]bool{})
			file, err := parser.ParseFile(token.NewFileSet(), "generated.go", b.String(), 0)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			ast.Inspect(file, func(node ast.Node) bool {
				if spec, ok := node.(*ast.ValueSpec); ok {
					for _, id := range spec.Names {
						if names[id.Name] {
							t.Fatalf("duplicate constant %s", id.Name)
						}
						names[id.Name] = true
					}
				}
				return true
			})
			if len(names) != 2 || !strings.Contains(b.String(), `"openai/form"`) || !strings.Contains(b.String(), `"openaiForm"`) {
				t.Fatalf("wire enum lost: %s", b.String())
			}
		})
	}
}
