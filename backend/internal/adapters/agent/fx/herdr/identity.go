// Package herdr receives fx's native lifecycle reports over AO's local socket.
package herdr

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// SocketPath keeps the daemon's socket inside its configured data directory.
func SocketPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "fx-herdr.sock")
}

// PaneID binds fx's native pane identity to one AO session and runtime launch.
func PaneID(sessionID domain.SessionID, launchID string) string {
	return "ao:1:" + base64.RawURLEncoding.EncodeToString([]byte(sessionID)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(launchID))
}

func decodePaneID(pane string) (domain.SessionID, string, bool) {
	parts := strings.Split(pane, ":")
	if len(parts) != 4 || parts[0] != "ao" || parts[1] != "1" {
		return "", "", false
	}
	values := [2]string{}
	for i, part := range parts[2:] {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(part)
		value := string(decoded)
		if err != nil || value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return "", "", false
		}
		values[i] = value
	}
	return domain.SessionID(values[0]), values[1], true
}
