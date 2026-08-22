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
