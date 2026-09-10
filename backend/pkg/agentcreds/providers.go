package agentcreds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Provider is the API surface a credential belongs to. Claude Code selects one
// via apiProvider, and they are not interchangeable: the endpoint, the auth
// header, and even the model ID format differ between them.
type Provider string

const (
	// ProviderFirstParty is api.anthropic.com.
	ProviderFirstParty Provider = "firstParty"
	// ProviderGateway is an ANTHROPIC_BASE_URL-fronted proxy.
	ProviderGateway Provider = "gateway"
	// ProviderFoundry is Azure AI Foundry.
	ProviderFoundry Provider = "foundry"
	// ProviderBedrock is AWS Bedrock.
	ProviderBedrock Provider = "bedrock"
	// ProviderVertex is Google Vertex AI.
	ProviderVertex Provider = "vertex"
)

// ParseProvider normalizes the apiProvider string Claude Code reports.
// An unrecognized value yields ok=false, which callers must treat as "not our
// business" rather than guessing a default — probing the wrong provider sends
// a credential to a host that should never have seen it.
func ParseProvider(value string) (Provider, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "firstparty", "anthropic", "first_party":
		return ProviderFirstParty, true
	case "gateway", "proxy":
		return ProviderGateway, true
	case "foundry", "azure":
		return ProviderFoundry, true
	case "bedrock", "aws":
		return ProviderBedrock, true
	case "vertex", "gcp", "google":
		return ProviderVertex, true
	default:
		return "", false
	}
}

// endpointOverrides redirects a provider's base URL, for tests. Production
// code leaves every field empty.
type endpointOverrides struct {
	anthropic string
	bedrock   string
	vertex    string
	foundry   string
	sts       string
}

// requestFor builds the authenticated request for one provider.
func (v *Validator) requestFor(ctx context.Context, provider Provider, cred Credential) (requestSpec, error) {
	switch provider {
	case ProviderFirstParty, ProviderGateway:
		return v.anthropicRequest(ctx, provider, cred)
	case ProviderFoundry:
		return v.foundryRequest(ctx, cred)
	case ProviderBedrock:
		return v.bedrockRequest(ctx, cred)
	case ProviderVertex:
		return v.vertexRequest(ctx, cred)
	default:
		return requestSpec{}, fmt.Errorf("agentcreds: unsupported provider %q", provider)
	}
}

// anthropicRequest builds GET {base}/v1/models for the first-party API or a
// gateway fronting it. All five first-party credential sources share this one
// request and differ only in which header carries the secret.
func (v *Validator) anthropicRequest(ctx context.Context, provider Provider, cred Credential) (requestSpec, error) {
	base := firstNonEmpty(cred.BaseURL, v.endpoint.anthropic, "https://api.anthropic.com")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/models", http.NoBody)
	if err != nil {
		return requestSpec{}, err
	}
	request.Header.Set("anthropic-version", anthropicAPIVersion)
	if err := setAnthropicAuth(request, cred); err != nil {
		return requestSpec{}, err
	}
	label := "Anthropic"
	if provider == ProviderGateway {
		label = "the configured gateway"
	}
	return requestSpec{
		request:     request,
		parseModels: parseAnthropicModels,
		// A gateway need not implement model listing, and the first-party API
		// always does, so neither case treats an empty list as a rejection.
		requireModels: false,
		label:         label,
	}, nil
}

// setAnthropicAuth attaches the secret under the header its Kind requires.
func setAnthropicAuth(request *http.Request, cred Credential) error {
	secret := strings.TrimSpace(cred.Secret)
	if secret == "" {
		// Sending an empty header would come back as "x-api-key header is
		// required", which reads exactly like a rejected credential. Refuse
		// here so the caller reports unknown rather than unauthorized.
		return fmt.Errorf("agentcreds: no secret for %s credential from %s", cred.Kind, cred.Source)
	}
	switch cred.Kind {
	case KindAPIKey:
		request.Header.Set("x-api-key", secret)
	case KindOAuthToken, KindAuthToken:
		request.Header.Set("authorization", "Bearer "+secret)
	default:
		return fmt.Errorf("agentcreds: credential kind %q cannot authenticate to Anthropic", cred.Kind)
	}
	return nil
}

// foundryRequest builds the Azure AI Foundry probe. Foundry accepts either an
// api-key header or a bearer token, mirroring its two credential env vars.
func (v *Validator) foundryRequest(ctx context.Context, cred Credential) (requestSpec, error) {
	secret := strings.TrimSpace(cred.Secret)
	if secret == "" {
		return requestSpec{}, fmt.Errorf("agentcreds: no Foundry secret from %s", cred.Source)
	}
	base := cred.BaseURL
	if base == "" {
		base = v.endpoint.foundry
	}
	if base == "" {
		resource := strings.TrimSpace(cred.Resource)
		if resource == "" {
			return requestSpec{}, fmt.Errorf("agentcreds: Foundry needs a resource name")
		}
		base = fmt.Sprintf("https://%s.services.ai.azure.com/anthropic", resource)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/models", http.NoBody)
	if err != nil {
		return requestSpec{}, err
	}
	request.Header.Set("anthropic-version", anthropicAPIVersion)
	switch cred.Kind {
	case KindAzureAPIKey, KindAPIKey:
		request.Header.Set("api-key", secret)
	case KindAuthToken, KindOAuthToken:
		request.Header.Set("authorization", "Bearer "+secret)
	default:
		return requestSpec{}, fmt.Errorf("agentcreds: credential kind %q cannot authenticate to Foundry", cred.Kind)
	}
	return requestSpec{
		request: request, parseModels: parseAnthropicModels,
		requireModels: true, label: "Azure AI Foundry",
	}, nil
}

// parseAnthropicModels reads the first-party model list, which Foundry and
// gateways mirror.
//
// It also reads capabilities.effort, which is the only authoritative source for
// which reasoning levels a given model accepts. Those differ across the catalog
// and change as models ship, so they are carried through rather than assumed.
func parseAnthropicModels(body []byte) ([]Model, error) {
	var payload struct {
		Data []struct {
			ID           string `json:"id"`
			DisplayName  string `json:"display_name"`
			Capabilities struct {
				Effort map[string]json.RawMessage `json:"effort"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if !isClaudeModelID(entry.ID) {
			continue
		}
		models = append(models, Model{
			ID:          entry.ID,
			DisplayName: strings.TrimSpace(entry.DisplayName),
			Efforts:     supportedEfforts(entry.Capabilities.Effort),
		})
	}
	return models, nil
}

// effortOrder is the provider's own ascending order. The API reports effort as
// an object rather than a list, so the order has to be imposed here; presenting
// reasoning levels in map order would shuffle them on every request.
var effortOrder = []string{"low", "medium", "high", "xhigh", "max"}

// supportedEfforts extracts the levels a model actually accepts.
//
// The shape is {"supported": true, "low": {"supported": true}, ...}. A model
// with "supported": false takes no effort setting, and callers must render no
// control at all rather than a disabled or empty one.
func supportedEfforts(raw map[string]json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var supported bool
	if value, ok := raw["supported"]; ok {
		if json.Unmarshal(value, &supported) != nil || !supported {
			return nil
		}
	}
	efforts := make([]string, 0, len(effortOrder))
	for _, level := range effortOrder {
		value, ok := raw[level]
		if !ok {
			continue
		}
		var entry struct {
			Supported bool `json:"supported"`
		}
		if json.Unmarshal(value, &entry) == nil && entry.Supported {
			efforts = append(efforts, level)
		}
	}
	return efforts
}

// isClaudeModelID reports whether a provider's model ID names a Claude model.
//
// Each provider spells the same model differently — claude-opus-4-5-20251101
// first-party, anthropic.claude-…-v1:0 with a region prefix on Bedrock,
// claude-opus-4-5@20251101 on Vertex — so this matches the one substring they
// all share rather than trying to translate between the formats. Nothing maps
// between them by string rule, which is exactly why the call that validates
// must also be the call that supplies the catalog.
func isClaudeModelID(id string) bool {
	return strings.Contains(strings.ToLower(id), "claude")
}

// providerErrorMessage digs the human-readable reason out of a rejection body,
// across the several shapes the providers use.
func providerErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, candidate := range []string{payload.Error.Message, payload.Message, payload.Error.Code} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
