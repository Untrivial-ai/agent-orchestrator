package vmbrowser

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// CommandError is a stable browser failure surfaced to the HTTP layer with the
// same codes the desktop Electron runtime uses.
type CommandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CommandError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}

// asCommandError unwraps a CommandError for code-based branching.
func asCommandError(err error, target **CommandError) bool {
	return errors.As(err, target)
}

func invalidArgument(message string) error {
	return &CommandError{Code: "INVALID_ARGUMENT", Message: message}
}

// NativeArguments translates a service action plus its JSON args into the
// agent-browser argv form. It is a line-by-line port of
// nativeArgumentsForAction in frontend/src/main/agent-browser-runtime.ts; the
// TypeScript is the source of truth, so keep the branches identical. Argument
// errors here are INVALID_ARGUMENT, exactly like the port; the friendlier
// URL_REQUIRED / REFERENCE_REQUIRED / TAB_ID_REQUIRED codes come from the
// service layer's host-equivalent pre-validation.
func NativeArguments(action string, args map[string]any) ([]string, error) {
	ref := func() (string, error) {
		value, err := stringValue(args["ref"], "ref is required", false)
		if err != nil {
			return "", err
		}
		return nativeRef(value), nil
	}
	switch action {
	case "open":
		value, err := stringValue(args["url"], "url is required", false)
		if err != nil {
			return nil, err
		}
		if err := assertHTTPURL(value); err != nil {
			return nil, err
		}
		return []string{"open", value}, nil
	case "snapshot":
		out := []string{"snapshot"}
		if interactive, ok := args["interactive"].(bool); ok && interactive {
			out = append(out, "--interactive")
		}
		return append(out, "--compact"), nil
	case "click", "dblclick", "focus", "hover", "highlight", "scrollintoview", "check", "uncheck":
		value, err := ref()
		if err != nil {
			return nil, err
		}
		return []string{action, value}, nil
	case "fill", "type":
		value, err := ref()
		if err != nil {
			return nil, err
		}
		text, err := stringValue(args["text"], "text is required", true)
		if err != nil {
			return nil, err
		}
		return []string{action, value, text}, nil
	case "press":
		key, err := stringValue(args["key"], "key is required", false)
		if err != nil {
			return nil, err
		}
		return []string{"press", key}, nil
	case "drag":
		value, err := ref()
		if err != nil {
			return nil, err
		}
		target, err := stringValue(args["targetRef"], "target ref is required", false)
		if err != nil {
			return nil, err
		}
		return []string{"drag", value, nativeRef(target)}, nil
	case "select":
		value, err := ref()
		if err != nil {
			return nil, err
		}
		option, err := stringValue(args["value"], "value is required", true)
		if err != nil {
			return nil, err
		}
		return []string{"select", value, option}, nil
	case "tabs":
		return []string{"tab", "list"}, nil
	case "tab-new":
		var out []string
		if raw, ok := optionalStringValue(args["url"]); ok {
			if err := assertHTTPURL(raw); err != nil {
				return nil, err
			}
			out = append(out, raw)
		}
		return append([]string{"tab", "new"}, out...), nil
	case "tab-select":
		value, err := stringValue(args["tabId"], "tabId is required", false)
		if err != nil {
			return nil, err
		}
		return []string{"tab", value}, nil
	case "tab-close":
		var out []string
		if raw, ok := optionalStringValue(args["tabId"]); ok {
			out = append(out, raw)
		}
		return append([]string{"tab", "close"}, out...), nil
	case "scroll":
		direction, err := stringValue(args["direction"], "direction is required", false)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(direction) {
		case "up", "down", "left", "right":
		default:
			return nil, invalidArgument("direction must be up, down, left, or right")
		}
		amount, err := numberValue(args["amount"], 600, 1, 5_000)
		if err != nil {
			return nil, err
		}
		return []string{"scroll", strings.ToLower(direction), strconv.Itoa(amount)}, nil
	case "get":
		property, err := stringValue(args["property"], "property is required", false)
		if err != nil {
			return nil, err
		}
		property = strings.ToLower(property)
		switch property {
		case "url", "title", "text", "value", "checked":
		default:
			return nil, invalidArgument(fmt.Sprintf("Unsupported browser property: %s", property))
		}
		target, hasTarget := optionalStringValue(args["ref"])
		if (property == "url" || property == "title") && hasTarget {
			return nil, invalidArgument(fmt.Sprintf("%s does not accept an element ref", property))
		}
		if (property == "value" || property == "checked") && !hasTarget {
			return nil, invalidArgument(fmt.Sprintf("%s requires an element ref", property))
		}
		out := []string{"get", property}
		if hasTarget {
			out = append(out, nativeRef(target))
		}
		return out, nil
	case "wait":
		return nativeWaitArguments(args)
	case "frame":
		target, err := stringValue(args["target"], "frame target is required", false)
		if err != nil {
			return nil, err
		}
		if target == "main" {
			return []string{"frame", target}, nil
		}
		return []string{"frame", nativeRef(target)}, nil
	case "dialog":
		operation, err := stringValue(args["operation"], "dialog operation is required", false)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(operation) {
		case "accept", "dismiss", "status":
		default:
			return nil, invalidArgument("dialog operation must be accept, dismiss, or status")
		}
		var out []string
		if text, ok := optionalStringValue(args["text"]); ok {
			out = append(out, text)
		}
		return append([]string{"dialog", strings.ToLower(operation)}, out...), nil
	case "console", "errors":
		return []string{action}, nil
	default:
		return nil, invalidArgument(fmt.Sprintf("Unsupported native browser action: %s", action))
	}
}

func nativeWaitArguments(args map[string]any) ([]string, error) {
	timeout := strconv.Itoa(mustNumberValue(args["timeoutMs"], 10_000, 1, 55_000))
	if text, ok := args["text"].(string); ok && text != "" {
		return []string{"wait", "--text", text, "--timeout", timeout}, nil
	}
	if textGone, ok := args["textGone"].(string); ok && textGone != "" {
		return []string{"wait", "text=" + textGone, "--state", "hidden", "--timeout", timeout}, nil
	}
	if selector, ok := args["selector"].(string); ok && selector != "" {
		return []string{"wait", selector, "--timeout", timeout}, nil
	}
	if selectorGone, ok := args["selectorGone"].(string); ok && selectorGone != "" {
		return []string{"wait", selectorGone, "--state", "detached", "--timeout", timeout}, nil
	}
	if raw, ok := args["url"].(string); ok && raw != "" {
		return []string{"wait", "--url", "**" + raw + "**", "--timeout", timeout}, nil
	}
	if load, ok := args["load"].(bool); ok && load {
		return []string{"wait", "--load", "load", "--timeout", timeout}, nil
	}
	if stable, ok := numericArg(args["stableMs"]); ok && stable > 0 {
		stableMs := mustNumberValue(stable, 500, 1, 60_000)
		expression := fmt.Sprintf(`(() => { const key = "__aoDomStability"; const now = performance.now(); let state = globalThis[key]; if (!state) { state = { lastMutation: now }; state.observer = new MutationObserver(() => { state.lastMutation = performance.now(); }); state.observer.observe(document, { subtree: true, childList: true, attributes: true, characterData: true }); globalThis[key] = state; } if (performance.now() - state.lastMutation < %d) return false; state.observer.disconnect(); delete globalThis[key]; return true; })()`, stableMs)
		return []string{"wait", "--fn", expression, "--timeout", timeout}, nil
	}
	if ms, ok := numericArg(args["ms"]); ok && ms > 0 {
		return []string{"wait", strconv.Itoa(int(ms))}, nil
	}
	return nil, invalidArgument("A wait condition is required")
}

// numericArg coerces JSON-decoded and native numeric literals to float64.
func numericArg(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

var nativeRefPattern = regexp.MustCompile(`^@?e\d+$`)

func nativeRef(value string) string {
	if nativeRefPattern.MatchString(strings.ToLower(value)) {
		return "@" + strings.TrimPrefix(value, "@")
	}
	return value
}

func stringValue(value any, message string, allowEmpty bool) (string, error) {
	text, ok := value.(string)
	if !ok || (!allowEmpty && strings.TrimSpace(text) == "") {
		return "", invalidArgument(message)
	}
	if allowEmpty {
		return text, nil
	}
	return strings.TrimSpace(text), nil
}

func optionalStringValue(value any) (string, bool) {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text), true
	}
	return "", false
}

func numberValue(value any, fallback, minimum, maximum int) (int, error) {
	if !present(value) {
		return fallback, nil
	}
	var number float64
	switch n := value.(type) {
	case float64:
		number = n
	case int:
		number = float64(n)
	case int64:
		number = float64(n)
	default:
		return 0, invalidArgument(fmt.Sprintf("Numeric argument must be between %d and %d", minimum, maximum))
	}
	if math.IsNaN(number) || number < float64(minimum) || number > float64(maximum) {
		return 0, invalidArgument(fmt.Sprintf("Numeric argument must be between %d and %d", minimum, maximum))
	}
	return int(math.Round(number)), nil
}

func mustNumberValue(value any, fallback, minimum, maximum int) int {
	number, err := numberValue(value, fallback, minimum, maximum)
	if err != nil {
		return fallback
	}
	return number
}

func present(value any) bool {
	return value != nil
}

func assertHTTPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || strings.Contains(raw, " ") {
		return &CommandError{Code: "INVALID_URL", Message: "agent-browser navigation requires an explicit HTTP(S) URL"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return &CommandError{
			Code:    "BROWSER_URL_FORBIDDEN",
			Message: fmt.Sprintf("Unsupported browser URL scheme: %s", parsed.Scheme),
		}
	}
	if parsed.Host == "" {
		return &CommandError{Code: "INVALID_URL", Message: "agent-browser navigation requires an explicit HTTP(S) URL"}
	}
	return nil
}
