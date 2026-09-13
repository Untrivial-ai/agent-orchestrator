package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// sessionIDPattern matches runtime-safe session ids across runtimes (tmux,
// conpty). Dots are legal in session ids because they derive from project
// names, so ids need sanitising — not rejecting — before they become runtime
// handles.
var sessionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// RuntimeHandleName returns the runtime-safe name for a session id, applying
// the shared runtime sanitisation. Callers building runtime handles from raw
// session ids (attach hints, per-worker panes) must use this rather than the
// raw id, which can contain dots and other characters the runtimes' validators
// reject.
func RuntimeHandleName(id string) string {
	if sessionIDPattern.MatchString(id) && len(id) <= 48 {
		return id
	}
	return sanitizedSessionName(id)
}

func sanitizedSessionName(raw string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "session"
	}
	if len(base) > 32 {
		base = strings.TrimRight(base[:32], "-")
	}
	sum := sha256.Sum256([]byte(raw))
	return base + "-" + hex.EncodeToString(sum[:4])
}
