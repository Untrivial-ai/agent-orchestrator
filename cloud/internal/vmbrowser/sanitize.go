package vmbrowser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Ports of the Electron host's output sanitizers (browser-view-host.ts
// sanitizeBrowserURL / sanitizeBrowserTitle / sanitizeURLsInText /
// externalText / markUntrusted). Every URL or title placed in agent-visible
// results passes through these so cloud transcripts never retain credentials
// the desktop would have redacted.

const maxExternalTextBytes = 1 << 20

const (
	untrustedBegin = "<<<BEGIN UNTRUSTED EXTERNAL CONTENT>>>"
	untrustedEnd   = "<<<END UNTRUSTED EXTERNAL CONTENT>>>"
)

// SanitizeBrowserURL returns the safe-to-retain form of a URL: userinfo and
// fragment removed, every query parameter value redacted, non-http(s)/file
// protocols reduced to "scheme[redacted]".
func SanitizeBrowserURL(raw string) string {
	if raw == "" {
		return ""
	}
	if raw == "about:blank" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		withoutFragment := strings.Split(raw, "#")[0]
		withoutQuery := strings.Split(withoutFragment, "?")[0]
		credentialPattern := regexp.MustCompile(`^(?i)([a-z][a-z\d+.-]*://)[^/@\s]+@`)
		stripped := credentialPattern.ReplaceAllString(withoutQuery, "$1")
		if len(stripped) > 2_000 {
			stripped = stripped[:2_000]
		}
		return stripped
	}
	switch parsed.Scheme {
	case "http", "https", "file":
	default:
		return parsed.Scheme + "[redacted]"
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	query := parsed.Query()
	for name := range query {
		query.Set(name, "[redacted]")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

var urlInTextPattern = regexp.MustCompile(`(?i)\b(?:https?|file)://[^\s<>"']+`)

// SanitizeURLsInText redacts credentials inside every URL embedded in text.
func SanitizeURLsInText(raw string) string {
	return urlInTextPattern.ReplaceAllStringFunc(raw, SanitizeBrowserURL)
}

var urlLikeTitle = regexp.MustCompile(`^(?i)(?:[a-z][a-z\d+.-]*://|(?:about|mailto|data|javascript|blob):)`)

// SanitizeBrowserTitle sanitizes titles that are or look like URLs.
func SanitizeBrowserTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if urlLikeTitle.MatchString(title) {
		return SanitizeBrowserURL(title)
	}
	return SanitizeURLsInText(raw)
}

// ExternalText caps untrusted text at max bytes with a visible marker.
func ExternalText(value string) string {
	if len(value) <= maxExternalTextBytes {
		return value
	}
	return value[:maxExternalTextBytes] + fmt.Sprintf("\n[Content truncated at %d bytes]", maxExternalTextBytes)
}

// MarkUntrusted wraps page-controlled text in the trust boundary markers,
// escaping exact marker collisions so page content cannot forge a boundary.
func MarkUntrusted(value string) string {
	value = strings.ReplaceAll(value, untrustedBegin, `\u003c`+untrustedBegin[1:])
	value = strings.ReplaceAll(value, untrustedEnd, `\u003c`+untrustedEnd[1:])
	return untrustedBegin + "\n" + value + "\n" + untrustedEnd
}

// URL normalization ports (normalizeAgentBrowserURL and helpers). Desktop
// accepts bare hostnames and localhost-like strings with a default scheme;
// VM-local dev servers depend on it.

var (
	windowsAbsolutePath = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)
	localhostLike       = regexp.MustCompile(`^(?i)(localhost|127(\.\d{1,3}){3}|0\.0\.0\.0|\[::1\])(:\d+)?([/?#]|$)`)
	explicitPortSuffix  = regexp.MustCompile(`:\d+$`)
	schemePrefix        = regexp.MustCompile(`^[a-zA-Z][a-zA-Z\d+.-]*:`)
	explicitHTTPScheme  = regexp.MustCompile(`^(?i)https?://`)
)

func isPosixAbsolutePath(raw string) bool { return strings.HasPrefix(raw, "/") }

// looksLikeHost treats input as a navigable host when the authority (before
// any path/query/fragment) is an IPv6 literal, carries an explicit :port, or
// has a dot (a domain).
func looksLikeHost(raw string) bool {
	authority := raw
	if index := strings.IndexAny(raw, "/?#"); index >= 0 {
		authority = raw[:index]
	}
	if authority == "" {
		return false
	}
	if strings.HasPrefix(authority, "[") && strings.Contains(authority, "]") {
		return true
	}
	if explicitPortSuffix.MatchString(authority) {
		return true
	}
	return strings.Contains(authority, ".")
}

// NormalizeAgentBrowserURL validates and normalizes a target URL for agent
// open/tab-new, producing the desktop error codes.
func NormalizeAgentBrowserURL(input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", &CommandError{Code: "URL_REQUIRED", Message: "url is required"}
	}
	if windowsAbsolutePath.MatchString(raw) || isPosixAbsolutePath(raw) ||
		strings.HasPrefix(strings.ToLower(raw), "file:") {
		return "", &CommandError{Code: "BROWSER_URL_FORBIDDEN", Message: "Agent browser commands cannot open local files"}
	}
	if !explicitHTTPScheme.MatchString(raw) && !localhostLike.MatchString(raw) && !looksLikeHost(raw) {
		return "", &CommandError{Code: "INVALID_URL", Message: "ao browser open requires an explicit http(s) URL or hostname"}
	}

	candidate := raw
	if !explicitHTTPScheme.MatchString(raw) {
		if localhostLike.MatchString(raw) {
			candidate = "http://" + raw
		} else if !strings.ContainsAny(raw, " \t") &&
			(schemePrefix.MatchString(raw) || looksLikeHost(raw)) {
			if schemePrefix.MatchString(raw) {
				candidate = raw
			} else {
				candidate = "https://" + raw
			}
		}
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", &CommandError{Code: "INVALID_URL", Message: "ao browser open requires an explicit http(s) URL or hostname"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", &CommandError{
			Code:    "BROWSER_URL_FORBIDDEN",
			Message: "Agent browser commands support only http(s) URLs",
		}
	}
	if parsed.Path == "" && parsed.RawPath == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

// NormalizeNativeMessages ports normalizeNativeMessages: accepts messages or
// value arrays, applies level fallbacks, caps, marks, and timestamps.
func NormalizeNativeMessages(result map[string]any, action string) map[string]any {
	raw := []any{}
	if messages, ok := result["messages"].([]any); ok {
		raw = messages
	} else if values, ok := result["value"].([]any); ok {
		raw = values
	}
	now := time.Now().UTC().Format(time.RFC3339)
	messages := make([]any, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			level := "log"
			if action == "errors" {
				level = "error"
			}
			messages = append(messages, map[string]any{
				"level":     level,
				"message":   MarkUntrusted(ExternalText(SanitizeURLsInText(text))),
				"timestamp": now,
			})
			continue
		}
		record, _ := item.(map[string]any)
		if record == nil {
			record = map[string]any{}
		}
		level := ""
		if value, ok := record["level"].(string); ok {
			level = value
		} else if value, ok := record["type"].(string); ok {
			level = value
		} else if action == "errors" {
			level = "error"
		} else {
			level = "log"
		}
		message := ""
		if value, ok := record["message"].(string); ok {
			message = value
		} else if value, ok := record["text"].(string); ok {
			message = value
		} else if encoded, err := json.Marshal(record); err == nil {
			message = string(encoded)
		}
		timestamp := now
		if value, ok := record["timestamp"].(string); ok {
			timestamp = value
		}
		messages = append(messages, map[string]any{
			"level":     level,
			"message":   MarkUntrusted(ExternalText(SanitizeURLsInText(message))),
			"timestamp": timestamp,
		})
	}
	return map[string]any{"messages": messages, "untrustedExternalContent": true}
}
