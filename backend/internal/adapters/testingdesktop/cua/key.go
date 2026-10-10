package cua

import "strings"

func keyChord(keys []string) (string, []string, error) {
	if len(keys) == 0 || len(keys) > 5 {
		return "", nil, refuse("invalid_key", "one key with up to four modifiers is required")
	}
	var key string
	modifiers := make([]string, 0, 4)
	seen := make(map[string]bool)
	for _, input := range keys {
		value := strings.ToLower(input)
		switch value {
		case "command", "meta":
			value = "cmd"
		case "control":
			value = "ctrl"
		case "option":
			value = "alt"
		case "enter":
			value = "return"
		}
		if seen[value] {
			return "", nil, refuse("invalid_key", "duplicate key in chord")
		}
		seen[value] = true
		switch value {
		case "cmd", "ctrl", "alt", "shift":
			modifiers = append(modifiers, value)
		default:
			if key != "" || !supportedKey(value) {
				return "", nil, refuse("invalid_key", "unsupported key or multiple non-modifier keys")
			}
			key = value
		}
	}
	if key == "" {
		return "", nil, refuse("invalid_key", "modifier-only chords are not supported")
	}
	return key, modifiers, nil
}

func supportedKey(value string) bool {
	if len(value) == 1 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= '0' && value[0] <= '9')) {
		return true
	}
	switch value {
	case "return", "tab", "space", "escape", "backspace", "delete", "left", "right", "up", "down", "home", "end", "pageup", "pagedown",
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		return true
	default:
		return false
	}
}
