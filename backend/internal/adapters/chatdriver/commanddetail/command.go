// Package commanddetail normalizes provider command metadata for AO's durable
// conversation activity contract.
package commanddetail

import "strings"

// UnwrapShell removes one conventional `<shell> -lc '<command>'` wrapper.
//
// It deliberately removes exactly one matching quote pair. Trimming every quote
// at the edges corrupts valid nested quoting (for example `echo "hi"`) and would
// make the audit timeline report a command different from the one that ran.
func UnwrapShell(command string) string {
	for _, flag := range []string{" -lc ", " -c "} {
		if idx := strings.Index(command, flag); idx > 0 && looksLikeShell(command[:idx]) {
			inner := strings.TrimSpace(command[idx+len(flag):])
			if len(inner) >= 2 && inner[0] == inner[len(inner)-1] && (inner[0] == '\'' || inner[0] == '"') {
				return inner[1 : len(inner)-1]
			}
			return inner
		}
	}
	return command
}

func looksLikeShell(prefix string) bool {
	base := prefix
	if idx := strings.LastIndex(prefix, "/"); idx >= 0 {
		base = prefix[idx+1:]
	}
	switch base {
	case "sh", "bash", "zsh", "dash", "fish":
		return true
	default:
		return false
	}
}
