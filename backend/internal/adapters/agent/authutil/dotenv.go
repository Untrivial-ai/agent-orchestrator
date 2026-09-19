package authutil

import (
	"errors"
	"strconv"
	"strings"
)

// ParseDotenv reads assignments, optional export prefixes, quoted values and
// comments. It never executes commands or interpolates environment references.
func ParseDotenv(data []byte) (map[string]string, error) {
	if len(data) > MaxFileSize {
		return nil, errors.New("dotenv exceeds limit")
	}
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[7:])
		}
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvKey(key) {
			return nil, errors.New("invalid dotenv assignment")
		}
		value, ok := dotenvValue(strings.TrimSpace(raw))
		if !ok {
			return nil, errors.New("invalid dotenv value")
		}
		result[key] = value
	}
	return result, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

func dotenvValue(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if raw[0] != '\'' && raw[0] != '"' {
		for i := range raw {
			if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
				return strings.TrimSpace(raw[:i]), true
			}
		}
		return raw, true
	}
	quote := raw[0]
	escaped := false
	for i := 1; i < len(raw); i++ {
		if quote == '"' && raw[i] == '\\' && !escaped {
			escaped = true
			continue
		}
		if raw[i] == quote && !escaped {
			tail := strings.TrimSpace(raw[i+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return "", false
			}
			if quote == '\'' {
				return raw[1:i], true
			}
			value, err := strconv.Unquote(raw[:i+1])
			return value, err == nil
		}
		escaped = false
	}
	return "", false
}
