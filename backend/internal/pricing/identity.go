// Package pricing owns shared lookup identity rules for usage pricing.
package pricing

import "strings"

// CanonicalProviderID normalizes a reported provider for exact catalog lookup.
func CanonicalProviderID(raw string) string {
	providerID := strings.ToLower(strings.TrimSpace(raw))
	if providerID == "z.ai" {
		return "zai"
	}
	return providerID
}

// UnidentifiedBillingRoute marks a Claude route the hook saw and could not
// name: ANTHROPIC_BASE_URL pointed somewhere, but not at a host AO bills
// against. It is deliberately not a provider — it is the difference between
// "no hook has run yet" and "a hook ran and the answer is not one of ours",
// which an empty hint cannot express and which decides whether the model that
// answered is admissible evidence at all.
const UnidentifiedBillingRoute = "unidentified"

// TrustedClaudeBillingProvider narrows one routing string to a billing identity
// AO is willing to record for a Claude session. Anthropic, z.ai, Bedrock and
// Vertex are the four routes Claude Code can take that AO can name; anything
// else is a string of unknown provenance, and recording it is worse than
// recording nothing, because billing_provider_id is write-once and an
// unrecognised value there is unreachable by every later repair.
//
// Codex is deliberately not narrowed this way. Its rollout reports the provider
// its own config selected, which is a durable fact about who billed the session
// even when no catalog can price it, and blanking it would let the model
// fallback bill an OpenRouter or Azure session at OpenAI list rates.
func TrustedClaudeBillingProvider(raw string) string {
	providerID := CanonicalProviderID(raw)
	switch providerID {
	case "anthropic", "zai", "bedrock", "vertex_ai":
		return providerID
	default:
		return ""
	}
}

// TrustedClaudeRoute narrows one routing string to what may be recorded on a
// binding: a provider AO bills against, or the sentinel saying a route was
// observed and is not one of them.
func TrustedClaudeRoute(raw string) string {
	if provider := TrustedClaudeBillingProvider(raw); provider != "" {
		return provider
	}
	if CanonicalProviderID(raw) == UnidentifiedBillingRoute {
		return UnidentifiedBillingRoute
	}
	return ""
}

// CanonicalModelID normalizes a reported model for exact provider-local lookup.
// It removes at most one exact canonical provider prefix.
func CanonicalModelID(providerID, raw string) string {
	modelID := strings.ToLower(strings.TrimSpace(raw))
	prefix := CanonicalProviderID(providerID)
	if prefix != "" {
		modelID = strings.TrimPrefix(modelID, prefix+"/")
	}
	return modelID
}
