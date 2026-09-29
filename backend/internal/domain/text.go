package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

var titleSmallWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "by": {}, "for": {},
	"in": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {}, "via": {},
}

// SanitizeControlChars removes control characters that are unsafe to deliver
// into a live terminal pane, while preserving the whitespace that legitimate
// multi-line text relies on (newline, carriage return, tab).
//
// Any text that reaches an agent's PTY must pass through here. The session
// runtime pastes messages straight into the live pane, so an unfiltered escape
// sequence (cursor control, screen clear, OSC) embedded in attacker-influenced
// content — a GitHub reviewer comment, a CI job log tail — would be interpreted
// by the terminal instead of read as plain text. Both the HTTP send endpoint
// and the lifecycle nudge path share this one definition so neither can drift
// into delivering raw control bytes.
func SanitizeControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// TitleCaseSessionTitle turns a task label into a readable card title while
// preserving acronyms such as API and PR. Short connector words stay lowercase
// except at the beginning, matching normal title-case conventions.
func TitleCaseSessionTitle(s string) string {
	words := strings.Fields(strings.TrimSpace(s))
	for i, word := range words {
		lower := strings.ToLower(word)
		if i > 0 {
			if _, ok := titleSmallWords[lower]; ok {
				words[i] = lower
				continue
			}
		}
		first, size := utf8.DecodeRuneInString(word)
		if first == utf8.RuneError && size == 0 {
			continue
		}
		if strings.ToLower(word) == word {
			words[i] = string(unicode.ToUpper(first)) + word[size:]
		}
	}
	return strings.Join(words, " ")
}
