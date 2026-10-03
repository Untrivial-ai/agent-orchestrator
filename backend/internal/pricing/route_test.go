package pricing

import "testing"

func TestClaudeRouteFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "default anthropic", want: "anthropic"},
		{name: "anthropic base url", env: map[string]string{"ANTHROPIC_BASE_URL": "https://api.anthropic.com/"}, want: "anthropic"},
		{name: "zai without scheme", env: map[string]string{"ANTHROPIC_BASE_URL": "api.z.ai/api/anthropic"}, want: "zai"},
		{name: "custom gateway", env: map[string]string{"ANTHROPIC_BASE_URL": "https://gateway.example"}, want: UnidentifiedBillingRoute},
		{name: "bedrock", env: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "true"}, want: "bedrock"},
		{name: "vertex outranks base url", env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_BASE_URL": "https://api.z.ai"}, want: "vertex_ai"},
		{name: "bedrock and vertex", env: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "CLAUDE_CODE_USE_VERTEX": "1"}, want: UnidentifiedBillingRoute},
		// Break caught: Foundry routes to Azure, which AO has no catalog for;
		// reading it as plain Anthropic would bill it at Anthropic list rates.
		{name: "foundry", env: map[string]string{"CLAUDE_CODE_USE_FOUNDRY": "1"}, want: UnidentifiedBillingRoute},
		{name: "disabled flag", env: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "0"}, want: "anthropic"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClaudeRouteFromEnv(func(key string) string { return test.env[key] })
			if got != test.want {
				t.Fatalf("route = %q, want %q", got, test.want)
			}
		})
	}
}
