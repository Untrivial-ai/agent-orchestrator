package agentlaunch

import (
	"slices"
	"strconv"
	"strings"
)

// CodexProxyProviderFor names the account helper as a managed Codex launch knows it, or nothing.
func CodexProxyProviderFor(env map[string]string) string {
	if env["AO_PROXY_ENDPOINT"] == "" && env["AO_PROXY_TICKET"] == "" {
		return ""
	}
	return "ao-managed"
}

// CodexProxyArgv points a managed Codex launch at the account helper; the ticket stays in the environment.
func CodexProxyArgv(argv []string, env map[string]string) []string {
	if len(argv) == 0 || CodexProxyProviderFor(env) == "" {
		return argv
	}
	at := len(argv)
	if i := slices.Index(argv[1:], "--"); i >= 0 {
		at = i + 1
	}
	return slices.Concat(argv[:at], []string{
		"-c", `model_provider="ao-managed"`,
		"-c", `model_providers.ao-managed.name="AO Account Manager"`,
		"-c", "model_providers.ao-managed.base_url=" + strconv.Quote(strings.TrimRight(env["AO_PROXY_ENDPOINT"], "/")+"/v1"),
		"-c", `model_providers.ao-managed.env_key="AO_PROXY_TICKET"`,
		"-c", `model_providers.ao-managed.wire_api="responses"`,
		"-c", `model_providers.ao-managed.requires_openai_auth=false`,
		"-c", `model_providers.ao-managed.supports_websockets=false`,
	}, argv[at:])
}
