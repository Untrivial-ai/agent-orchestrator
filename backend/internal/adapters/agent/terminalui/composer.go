// Package terminalui contains conservative helpers for interpreting an
// interactive-agent composer from bounded terminal output.
package terminalui

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const composerLookbackLines = 8

type styledRune struct {
	value rune
	dim   bool
	bold  bool
}

// PlainTerminalText removes terminal control sequences while preserving the
// visible text and line structure of a rendered capture. Provider adapters use
// this when matching UI chrome; composer parsing below still retains SGR state.
func PlainTerminalText(output string) string {
	lines := styledTerminalLines(output)
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = styledString(line)
	}
	return strings.Join(plain, "\n")
}

// PlainTerminalLines is PlainTerminalText split into visible rows.
func PlainTerminalLines(output string) []string {
	return strings.Split(PlainTerminalText(output), "\n")
}

// ComposerState is a conservative interpretation of one current composer.
// Unknown means the expected structure was incomplete or absent; it must not
// be treated as empty by a caller making a destructive decision.
type ComposerState uint8

// Composer states. Unknown is the fail-closed zero value.
const (
	ComposerUnknown ComposerState = iota
	ComposerEmpty
	ComposerDraft
)

// LastPromptIsEmptyOrDimPlaceholder returns true only when a prompt marker is
// present near the bottom of the terminal and every visible rune after it is
// either whitespace or rendered with SGR dim styling. Interactive agents use
// dim text for placeholder suggestions; normal text is a human-authored draft.
// Plain captures that lose styling therefore fail closed for non-empty text.
func LastPromptIsEmptyOrDimPlaceholder(output, marker string) bool {
	return LastPromptComposerState(output, marker) == ComposerEmpty
}

// LastPromptComposerState classifies a footer-free prompt composer. Normal
// visible text after the prompt is a draft; whitespace and dim provider
// placeholder text are empty. Missing or incomplete prompt evidence is
// unknown.
func LastPromptComposerState(output, marker string) ComposerState {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return ComposerUnknown
	}
	lines := styledTerminalLines(output)
	start := len(lines) - composerLookbackLines
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		line := trimLeftStyledSpace(lines[i])
		markerRunes := []rune(marker)
		if len(line) < len(markerRunes) || styledString(line[:len(markerRunes)]) != marker {
			continue
		}
		for _, r := range line[len(markerRunes):] {
			if unicode.IsSpace(r.value) {
				continue
			}
			if !r.dim {
				return ComposerDraft
			}
		}
		// Wrapped composer content is rendered on following rows without
		// repeating the prompt marker, and a draft may itself begin with blank
		// rows. Inspect every remaining captured row: whitespace and styled-dim
		// provider chrome are safe, while any ordinary visible rune fails closed.
		// This deliberately rejects a plain/un-styled footer because treating a
		// leading-newline human draft as chrome would be destructive.
		for j := i + 1; j < len(lines); j++ {
			continuation := lines[j]
			for _, r := range continuation {
				if unicode.IsSpace(r.value) {
					continue
				}
				if !r.dim {
					return ComposerDraft
				}
			}
		}
		return ComposerEmpty
	}
	return ComposerUnknown
}

// LastPromptHasBoldMarker recognizes a provider-owned current prompt marker
// without accepting the same visible text from plain scrollback. This is useful
// when a constrained TUI viewport omits its normal footer but retains the SGR
// styling that distinguishes current chrome from transcript content.
func LastPromptHasBoldMarker(output, marker string) bool {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return false
	}
	markerRunes := []rune(marker)
	lines := styledTerminalLines(output)
	start := len(lines) - composerLookbackLines
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		line := trimLeftStyledSpace(lines[i])
		if len(line) < len(markerRunes) || styledString(line[:len(markerRunes)]) != marker {
			continue
		}
		for _, r := range line[:len(markerRunes)] {
			if !r.bold || r.dim {
				return false
			}
		}
		return true
	}
	return false
}

// LastBorderedPromptIsEmptyOrDimPlaceholder recognizes providers that render
// the composer between matching full-width horizontal rules and place normal,
// non-dim status chrome below the lower rule. Only rows inside the bordered
// composer are considered input. Requiring both matching rules keeps the check
// fail-closed when a capture is partial or the provider changes its layout.
func LastBorderedPromptIsEmptyOrDimPlaceholder(output, marker string) bool {
	return LastBorderedPromptComposerState(output, marker) == ComposerEmpty
}

// LastBorderedPromptComposerState classifies only the content between a pair
// of matching composer rules. Provider status chrome below the lower rule is
// excluded. An incomplete border is unknown rather than empty.
func LastBorderedPromptComposerState(output, marker string) ComposerState {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return ComposerUnknown
	}
	lines := styledTerminalLines(output)
	markerRunes := []rune(marker)
	start := len(lines) - composerLookbackLines
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		line := trimLeftStyledSpace(lines[i])
		if len(line) < len(markerRunes) || styledString(line[:len(markerRunes)]) != marker {
			continue
		}
		upperWidth := 0
		for j := i - 1; j >= 0 && upperWidth == 0; j-- {
			upperWidth = horizontalRuleWidth(lines[j])
		}
		lowerIndex, lowerWidth := -1, 0
		for j := len(lines) - 1; j > i; j-- {
			if lowerWidth = horizontalRuleWidth(lines[j]); lowerWidth > 0 {
				lowerIndex = j
				break
			}
		}
		if upperWidth == 0 || lowerIndex < 0 || upperWidth != lowerWidth {
			return ComposerUnknown
		}
		for _, r := range line[len(markerRunes):] {
			if !unicode.IsSpace(r.value) && !r.dim {
				return ComposerDraft
			}
		}
		for _, continuation := range lines[i+1 : lowerIndex] {
			for _, r := range continuation {
				if !unicode.IsSpace(r.value) && !r.dim {
					return ComposerDraft
				}
			}
		}
		return ComposerEmpty
	}
	return ComposerUnknown
}

func horizontalRuleWidth(line []styledRune) int {
	for len(line) > 0 && unicode.IsSpace(line[0].value) {
		line = line[1:]
	}
	for len(line) > 0 && unicode.IsSpace(line[len(line)-1].value) {
		line = line[:len(line)-1]
	}
	if len(line) < 16 {
		return 0
	}
	for _, r := range line {
		if r.value != '─' {
			return 0
		}
	}
	return len(line)
}

func styledTerminalLines(output string) [][]styledRune {
	output = strings.ReplaceAll(output, "\r", "\n")
	lines := make([][]styledRune, 0, 1)
	lines = append(lines, nil)
	dim := false
	bold := false
	for i := 0; i < len(output); {
		if output[i] == '\x1b' {
			if next, params, sgr := consumeEscape(output, i); next > i {
				if sgr {
					dim = applySGRDim(dim, params)
					bold = applySGRBold(bold, params)
				}
				i = next
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(output[i:])
		i += size
		if r == '\n' {
			lines = append(lines, nil)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		lines[len(lines)-1] = append(lines[len(lines)-1], styledRune{value: r, dim: dim, bold: bold})
	}
	return lines
}

func consumeEscape(output string, start int) (next int, params string, sgr bool) {
	if start+1 >= len(output) {
		return start + 1, "", false
	}
	switch output[start+1] {
	case '[':
		for i := start + 2; i < len(output); i++ {
			b := output[i]
			if b < 0x40 || b > 0x7e {
				continue
			}
			return i + 1, output[start+2 : i], b == 'm'
		}
		return len(output), "", false
	case ']':
		for i := start + 2; i < len(output); i++ {
			if output[i] == '\a' {
				return i + 1, "", false
			}
			if output[i] == '\x1b' && i+1 < len(output) && output[i+1] == '\\' {
				return i + 2, "", false
			}
		}
		return len(output), "", false
	default:
		return min(start+2, len(output)), "", false
	}
}

func applySGRDim(current bool, params string) bool {
	if params == "" {
		return false
	}
	for _, raw := range strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' }) {
		code, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		switch code {
		case 0, 22:
			current = false
		case 2:
			current = true
		}
	}
	return current
}

func applySGRBold(current bool, params string) bool {
	if params == "" {
		return false
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		raw := fields[i]
		if colon := strings.IndexByte(raw, ':'); colon >= 0 {
			raw = raw[:colon]
		}
		code, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		// Extended foreground, background, and underline colors use either
		// 38:2:... / 38:5:... or semicolon-separated payloads. Their color values
		// are data, not independent SGR attributes (a palette index of 1 is not
		// bold). Skip the whole payload before examining later attributes.
		if code == 38 || code == 48 || code == 58 {
			if strings.Contains(fields[i], ":") || i+1 >= len(fields) {
				continue
			}
			switch fields[i+1] {
			case "5":
				i = min(i+2, len(fields)-1)
			case "2":
				i = min(i+4, len(fields)-1)
			}
			continue
		}
		switch code {
		case 0, 22:
			current = false
		case 1:
			current = true
		}
	}
	return current
}

func trimLeftStyledSpace(line []styledRune) []styledRune {
	for len(line) > 0 && unicode.IsSpace(line[0].value) {
		line = line[1:]
	}
	return line
}

func styledString(line []styledRune) string {
	var b strings.Builder
	for _, r := range line {
		b.WriteRune(r.value)
	}
	return b.String()
}
