// Package browsercontract is the single source of truth for the session
// browser command surface shared by the desktop daemon, the desktop CLI, and
// the cloud in-VM browser service. It is deliberately dependency-free.
package browsercontract

import (
	"sort"
	"strings"
)

const (
	// CapabilityHeader carries the per-session browser capability token.
	CapabilityHeader = "X-AO-Browser-Capability"
	// RouteCommands is the loopback browser command route.
	RouteCommands = "/api/v1/browser/commands"
	// RouteStatus is the loopback browser status route.
	RouteStatus = "/api/v1/browser/status"
)

var actions = map[string]struct{}{
	"open": {}, "snapshot": {}, "act": {}, "click": {}, "dblclick": {}, "focus": {}, "fill": {}, "type": {}, "press": {},
	"hover": {}, "highlight": {}, "unhighlight": {}, "scrollintoview": {}, "drag": {}, "tabs": {}, "tab-new": {},
	"tab-select": {}, "tab-close": {}, "scroll": {}, "select": {}, "check": {},
	"uncheck": {}, "get": {}, "wait": {}, "screenshot": {}, "network-start": {},
	"network-status": {}, "network-list": {}, "network-stop": {}, "network-clear": {},
	"console": {}, "errors": {}, "frame": {}, "dialog": {},
	"devtools-open": {}, "devtools-close": {},
}

// Supported reports whether action is an enabled browser action. Actions are
// matched case-insensitively and ignore surrounding whitespace, matching the
// daemon's normalization at the dispatch boundary.
func Supported(action string) bool {
	_, ok := actions[strings.ToLower(strings.TrimSpace(action))]
	return ok
}

// Actions returns the sorted action list (for docs and tests).
func Actions() []string {
	out := make([]string, 0, len(actions))
	for action := range actions {
		out = append(out, action)
	}
	sort.Strings(out)
	return out
}
