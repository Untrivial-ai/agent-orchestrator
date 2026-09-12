package agentruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

// CodexHook is an AO-authored command delivered through native session flags.
type CodexHook struct {
	Event   string
	Command string
	Timeout int
}

// CodexSessionHooks approves only the supplied AO-authored session-layer commands.
// Never pass repository or user definitions here. Codex reviews those separately.
// The source key and normalized hash are a native-provider contract, covered by
// the opt-in native tests. A provider change must not fall back to a global bypass.
func CodexSessionHooks(hooks []CodexHook) ([]string, error) {
	var args, states []string
	seen := map[string]bool{}
	for _, hook := range hooks {
		if seen[hook.Event] {
			return nil, fmt.Errorf("duplicate AO Codex hook event %q", hook.Event)
		}
		seen[hook.Event] = true
		key, hash, err := codexSessionHookIdentity(hook.Event, hook.Command, hook.Timeout)
		if err != nil {
			return nil, err
		}
		args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`, hook.Event, codexTOMLString(hook.Command), hook.Timeout))
		states = append(states, fmt.Sprintf(`%s={trusted_hash=%s}`, codexTOMLString(key), codexTOMLString(hash)))
	}
	if len(states) > 0 {
		// Set only trusted_hash; Codex preserves the user's enabled=false field.
		args = append(args, "-c", "hooks.state={"+strings.Join(states, ",")+"}")
	}
	return args, nil
}

func codexSessionHookIdentity(event, command string, timeout int) (string, string, error) {
	labels := map[string]string{
		"SessionStart": "session_start", "UserPromptSubmit": "user_prompt_submit",
		"PermissionRequest": "permission_request", "Stop": "stop",
	}
	label, ok := labels[event]
	if !ok {
		return "", "", fmt.Errorf("unsupported AO Codex hook event %q", event)
	}
	identity := map[string]any{
		"event_name": label,
		"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": timeout, "async": false,
		}},
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(identity); err != nil {
		return "", "", fmt.Errorf("encode Codex hook identity: %w", err)
	}
	// Rust's canonical JSON leaves these Unicode separators literal. Replace
	// only actual escape tokens, preserving literal backslash-u in commands.
	serialized := strings.TrimSuffix(canonical.String(), "\n")
	var normalized strings.Builder
	for i := 0; i < len(serialized); i++ {
		if serialized[i] == '\\' && i+1 < len(serialized) {
			if strings.HasPrefix(serialized[i:], `\u2028`) || strings.HasPrefix(serialized[i:], `\u2029`) {
				if serialized[i+5] == '8' {
					normalized.WriteRune('\u2028')
				} else {
					normalized.WriteRune('\u2029')
				}
				i += 5
				continue
			}
			normalized.WriteByte(serialized[i])
			i++
		}
		normalized.WriteByte(serialized[i])
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(normalized.String())))
	source := "/<session-flags>/config.toml"
	if runtime.GOOS == "windows" {
		source = `C:\<session-flags>\config.toml`
	}
	key := source + ":" + label + ":0:0"
	return key, hash, nil
}
