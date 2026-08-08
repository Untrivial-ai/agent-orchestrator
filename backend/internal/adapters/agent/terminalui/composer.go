// Package terminalui contains conservative helpers for recognizing an empty
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
}

// LastPromptIsEmptyOrDimPlaceholder returns true only when a prompt marker is
// present near the bottom of the terminal and every visible rune after it is
// either whitespace or rendered with SGR dim styling. Interactive agents use
// dim text for placeholder suggestions; normal text is a human-authored draft.
// Plain captures that lose styling therefore fail closed for non-empty text.
func LastPromptIsEmptyOrDimPlaceholder(output, marker string) bool {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return false
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
				return false
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
					return false
				}
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
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return false
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
		for _, r := range line[len(markerRunes):] {
			if !unicode.IsSpace(r.value) && !r.dim {
				return false
			}
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
			return false
		}
		for _, continuation := range lines[i+1 : lowerIndex] {
			for _, r := range continuation {
				if !unicode.IsSpace(r.value) && !r.dim {
					return false
				}
			}
		}
		return true
	}
	return false
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
	for i := 0; i < len(output); {
		if output[i] == '\x1b' {
			if next, params, sgr := consumeEscape(output, i); next > i {
				if sgr {
					dim = applySGRDim(dim, params)
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
		lines[len(lines)-1] = append(lines[len(lines)-1], styledRune{value: r, dim: dim})
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
