package pricing

import (
	"net/url"
	"strings"
)

// ClaudeRouteFromEnv names the billing route a Claude Code process launched
// with this environment will take. It is shared by the TUI hook, which reads
// the environment Claude Code handed it, and the Chat driver, which reads the
// environment it is about to launch Claude Code with, so both modes record the
// same route for the same configuration.
//
// The result is always recordable on a binding: a provider AO bills against,
// or UnidentifiedBillingRoute when the session is routed somewhere AO cannot
// name. It is never empty, because a launch environment is itself evidence of
// the route; an empty hint means only that no such evidence has arrived.
func ClaudeRouteFromEnv(lookup func(string) string) string {
	bedrock := claudeRouteFlagEnabled(lookup("CLAUDE_CODE_USE_BEDROCK"))
	vertex := claudeRouteFlagEnabled(lookup("CLAUDE_CODE_USE_VERTEX"))
	foundry := claudeRouteFlagEnabled(lookup("CLAUDE_CODE_USE_FOUNDRY"))
	switch {
	case foundry || bedrock && vertex:
		// Foundry has no catalog here, and more than one flag is a route that
		// is certainly not plain Anthropic. Either way this still has to rule
		// out inferring one from the model.
		return UnidentifiedBillingRoute
	case bedrock:
		return "bedrock"
	case vertex:
		return "vertex_ai"
	}
	baseURL := strings.TrimSpace(lookup("ANTHROPIC_BASE_URL"))
	if baseURL == "" {
		return "anthropic"
	}
	// A base URL AO cannot name still rules out inferring one from the model:
	// the session is routed somewhere, and reporting that is the difference
	// between "no route is known" and "the route is known and not ours".
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return UnidentifiedBillingRoute
	}
	if parsed.Hostname() == "" && !strings.Contains(baseURL, "://") {
		parsed, err = url.Parse("https://" + baseURL)
	}
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return UnidentifiedBillingRoute
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.anthropic.com":
		return "anthropic"
	case "api.z.ai":
		return "zai"
	default:
		return UnidentifiedBillingRoute
	}
}

func claudeRouteFlagEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
