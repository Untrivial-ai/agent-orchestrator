package chat

import (
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	mdtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The browser selects rendered text, whereas assistant messages are stored as
// Markdown. Verify against readable text from the durable source, never against
// a client-supplied rendering of that source.
func sourceContainsExcerptSelection(source domain.ConversationMessage, selection string) bool {
	needle := normalizeExcerptWhitespace(selection)
	if needle == "" {
		return false
	}
	if source.Role != domain.MessageRoleAssistant {
		return strings.Contains(normalizeExcerptWhitespace(source.Text), needle)
	}
	return strings.Contains(normalizeExcerptWhitespace(renderedExcerptText(source.Text)), needle)
}

func normalizeExcerptWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func renderedExcerptText(markdown string) string {
	source := []byte(markdown)
	root := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(mdtext.NewReader(source))
	var visible strings.Builder
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if node.Type() == ast.TypeBlock {
			visible.WriteByte(' ')
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Image:
			// Image alt text is an attribute, not selectable chat text.
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			// React Markdown escapes raw HTML, leaving the markup selectable as text.
			for i := 0; i < n.Segments.Len(); i++ {
				segment := n.Segments.At(i)
				visible.Write(segment.Value(source))
			}
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				line := lines.At(i)
				visible.Write(line.Value(source))
			}
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			visible.WriteString(excerptTextValue(n.Value(source), n.IsRaw()))
			if n.SoftLineBreak() || n.HardLineBreak() {
				visible.WriteByte(' ')
			}
		case *ast.String:
			visible.WriteString(excerptTextValue(n.Value, n.IsRaw() || n.IsCode()))
		case *ast.AutoLink:
			visible.Write(n.Label(source))
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock, *ast.FencedCodeBlock:
			lines := node.Lines()
			for i := 0; i < lines.Len(); i++ {
				line := lines.At(i)
				visible.Write(line.Value(source))
				visible.WriteByte(' ')
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return visible.String()
}

func excerptTextValue(value []byte, raw bool) string {
	if raw {
		return string(value)
	}
	return html.UnescapeString(string(util.UnescapePunctuations(value)))
}
