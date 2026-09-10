package pricing

import "testing"

func TestCanonicalProviderIDNormalizesLookupIdentity(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trim and lowercase", raw: "  OpenAI  ", want: "openai"},
		{name: "z ai alias", raw: " Z.AI ", want: "zai"},
		{name: "already canonical", raw: "anthropic", want: "anthropic"},
		{name: "empty", raw: " \t ", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanonicalProviderID(test.raw); got != test.want {
				t.Fatalf("CanonicalProviderID(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestCanonicalModelIDStripsAtMostOneExactCanonicalProviderPrefix(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		raw        string
		want       string
	}{
		{name: "trim lowercase and strip", providerID: " OpenAI ", raw: " OpenAI/GPT-5.6 ", want: "gpt-5.6"},
		{name: "strip once", providerID: "openai", raw: "openai/openai/gpt-5.6", want: "openai/gpt-5.6"},
		{name: "cross provider stays exact", providerID: "openai", raw: "anthropic/claude-sonnet-4", want: "anthropic/claude-sonnet-4"},
		{name: "partial prefix stays exact", providerID: "openai", raw: "openai-compatible/gpt-5.6", want: "openai-compatible/gpt-5.6"},
		{name: "canonical alias provider prefix", providerID: "z.ai", raw: "ZAI/GLM-4.5", want: "glm-4.5"},
		{name: "noncanonical model prefix stays", providerID: "z.ai", raw: "z.ai/glm-4.5", want: "z.ai/glm-4.5"},
		{name: "empty provider only normalizes model", raw: " Vendor/Model ", want: "vendor/model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanonicalModelID(test.providerID, test.raw); got != test.want {
				t.Fatalf("CanonicalModelID(%q, %q) = %q, want %q", test.providerID, test.raw, got, test.want)
			}
		})
	}
}
