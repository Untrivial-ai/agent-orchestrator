package agentcreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The provider gate exists so a credential is never sent to a host that should
// not see it. This is the test that would catch a Bedrock key being leaked to
// api.anthropic.com.
func TestProviderGateNeverProbesTheWrongHost(t *testing.T) {
	anthropic := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a non-first-party credential must never reach api.anthropic.com")
	}))
	defer anthropic.Close()

	bedrock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"modelSummaries":[{"modelId":"anthropic.claude-opus-4-5-v1:0","providerName":"Anthropic"}]}`))
	}))
	defer bedrock.Close()

	validator := New(WithHTTPClient(bedrock.Client()))
	validator.endpoint.anthropic = anthropic.URL

	result := validator.Validate(context.Background(), Credential{
		Kind: KindAuthToken, Secret: "bedrock-bearer", Source: "AWS_BEARER_TOKEN_BEDROCK",
		Provider: ProviderBedrock, Region: "us-east-1", BaseURL: bedrock.URL,
	})
	if !result.Valid() {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
}

// Every probe must hit a model-listing endpoint. A generic identity endpoint
// would confirm the credential authenticates and say nothing about whether it
// can reach Claude.
func TestProbesTargetModelEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		wantPath string
	}{
		{
			name:     "first party",
			cred:     Credential{Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty},
			wantPath: "/v1/models",
		},
		{
			name:     "gateway",
			cred:     Credential{Kind: KindAPIKey, Secret: "k", Provider: ProviderGateway},
			wantPath: "/v1/models",
		},
		{
			name:     "foundry",
			cred:     Credential{Kind: KindAzureAPIKey, Secret: "k", Provider: ProviderFoundry, Resource: "r"},
			wantPath: "/v1/models",
		},
		{
			name:     "bedrock",
			cred:     Credential{Kind: KindAuthToken, Secret: "k", Provider: ProviderBedrock, Region: "us-east-1"},
			wantPath: "/foundation-models",
		},
		{
			name: "vertex",
			cred: Credential{
				Kind: KindGoogleAccessToken, Secret: "k", Provider: ProviderVertex,
				Region: "us-east5", Project: "proj",
			},
			wantPath: "/publishers/anthropic/models",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"}],"modelSummaries":[{"modelId":"anthropic.claude-x"}],` +
					`"publisherModels":[{"name":"publishers/anthropic/models/claude-x"}]}`))
			}))
			defer server.Close()

			cred := tc.cred
			cred.BaseURL = server.URL
			New(WithHTTPClient(server.Client())).Validate(context.Background(), cred)
			if !strings.HasSuffix(path, tc.wantPath) {
				t.Fatalf("probed %q, want a path ending in %q", path, tc.wantPath)
			}
			// The runtime endpoint bills tokens; the control plane does not.
			if strings.Contains(path, "bedrock-runtime") || strings.Contains(path, ":messages") {
				t.Fatalf("probe hit a billable endpoint: %q", path)
			}
		})
	}
}

// For the cloud providers, authenticating is not the same as having Claude.
// A 200 with no Anthropic models means the account will fail on its first turn.
func TestCloudProvidersRequireClaudeEntitlement(t *testing.T) {
	tests := []struct {
		name string
		cred Credential
		body string
	}{
		{
			name: "bedrock without anthropic models",
			cred: Credential{Kind: KindAuthToken, Secret: "k", Provider: ProviderBedrock, Region: "us-east-1"},
			body: `{"modelSummaries":[{"modelId":"amazon.titan-text-v1","providerName":"Amazon"}]}`,
		},
		{
			name: "bedrock with an empty list",
			cred: Credential{Kind: KindAuthToken, Secret: "k", Provider: ProviderBedrock, Region: "us-east-1"},
			body: `{"modelSummaries":[]}`,
		},
		{
			name: "vertex without anthropic publishers",
			cred: Credential{
				Kind: KindGoogleAccessToken, Secret: "k", Provider: ProviderVertex,
				Region: "us-east5", Project: "p",
			},
			body: `{"publisherModels":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			cred := tc.cred
			cred.BaseURL = server.URL
			result := New(WithHTTPClient(server.Client())).Validate(context.Background(), cred)
			if result.State != StateUnknown {
				t.Fatalf("state = %q (%s), want unknown — a 200 without Claude access is not a pass",
					result.State, result.Detail)
			}
		})
	}
}

// A gateway need not implement model listing, so an empty list there is not a
// verdict either way, and a 404 is Unknown rather than a rejection.
func TestGatewayToleratesAMissingModelEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
		Kind: KindAPIKey, Secret: "k", Provider: ProviderGateway, BaseURL: server.URL,
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// Model IDs do not translate between providers, so the validating call must
// also be what supplies the catalog.
func TestValidationReturnsThatProvidersModelIDs(t *testing.T) {
	tests := []struct {
		name string
		cred Credential
		body string
		want string
	}{
		{
			name: "first party format",
			cred: Credential{Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty},
			body: `{"data":[{"id":"claude-opus-4-5-20251101"},{"id":"gpt-4o"}]}`,
			want: "claude-opus-4-5-20251101",
		},
		{
			name: "bedrock format",
			cred: Credential{Kind: KindAuthToken, Secret: "k", Provider: ProviderBedrock, Region: "us-east-1"},
			body: `{"modelSummaries":[{"modelId":"us.anthropic.claude-opus-4-5-v1:0","providerName":"Anthropic"}]}`,
			want: "us.anthropic.claude-opus-4-5-v1:0",
		},
		{
			name: "vertex format",
			cred: Credential{
				Kind: KindGoogleAccessToken, Secret: "k", Provider: ProviderVertex,
				Region: "us-east5", Project: "p",
			},
			body: `{"publisherModels":[{"name":"publishers/anthropic/models/claude-opus-4-5@20251101"}]}`,
			want: "claude-opus-4-5@20251101",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			cred := tc.cred
			cred.BaseURL = server.URL
			result := New(WithHTTPClient(server.Client())).Validate(context.Background(), cred)
			if !result.Valid() {
				t.Fatalf("state = %q (%s)", result.State, result.Detail)
			}
			if len(result.Models) != 1 || result.Models[0].ID != tc.want {
				t.Fatalf("models = %v, want exactly [%s]", result.Models, tc.want)
			}
		})
	}
}

// Foundry accepts either credential shape, under different headers.
func TestFoundryHeaderFollowsKind(t *testing.T) {
	tests := []struct {
		kind       Kind
		wantHeader string
		wantValue  string
	}{
		{KindAzureAPIKey, "Api-Key", "foundry-secret"},
		{KindAuthToken, "Authorization", "Bearer foundry-secret"},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			var got http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-4-5"}]}`))
			}))
			defer server.Close()

			result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
				Kind: tc.kind, Secret: "foundry-secret", Provider: ProviderFoundry, BaseURL: server.URL,
			})
			if !result.Valid() {
				t.Fatalf("state = %q (%s)", result.State, result.Detail)
			}
			if got.Get(tc.wantHeader) != tc.wantValue {
				t.Fatalf("%s = %q, want %q", tc.wantHeader, got.Get(tc.wantHeader), tc.wantValue)
			}
		})
	}
}

// Foundry needs a resource name to build a URL at all. Missing one is Unknown,
// not a rejection.
func TestFoundryWithoutAResourceIsUnknown(t *testing.T) {
	result := New().Validate(context.Background(), Credential{
		Kind: KindAzureAPIKey, Secret: "k", Provider: ProviderFoundry,
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// A credential kind that cannot authenticate to a provider is a programming
// error, and must surface as Unknown rather than as a bogus rejection.
func TestMismatchedKindAndProviderIsUnknown(t *testing.T) {
	result := New().Validate(context.Background(), Credential{
		Kind: KindAWSSigV4, Secret: "a\nb", Provider: ProviderFirstParty,
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// Effort levels differ per model and change as models ship, so they must come
// from the provider. This fixture is the real api.anthropic.com shape.
func TestEffortLevelsComeFromTheProvider(t *testing.T) {
	body := `{"data":[
		{"id":"claude-opus-5","display_name":"Claude Opus 5","capabilities":{"effort":{
			"supported":true,
			"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},
			"xhigh":{"supported":true},"max":{"supported":true}}}},
		{"id":"claude-opus-4-6","display_name":"Claude Opus 4.6","capabilities":{"effort":{
			"supported":true,
			"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},
			"max":{"supported":true}}}},
		{"id":"claude-opus-4-5-20251101","capabilities":{"effort":{
			"supported":true,
			"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true}}}},
		{"id":"claude-haiku-4-5-20251001","capabilities":{"effort":{"supported":false}}},
		{"id":"claude-sonnet-4-5-20250929","capabilities":{}}
	]}`
	models, err := parseAnthropicModels([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"claude-opus-5":              {"low", "medium", "high", "xhigh", "max"},
		"claude-opus-4-6":            {"low", "medium", "high", "max"},
		"claude-opus-4-5-20251101":   {"low", "medium", "high"},
		"claude-haiku-4-5-20251001":  nil,
		"claude-sonnet-4-5-20250929": nil,
	}
	if len(models) != len(want) {
		t.Fatalf("parsed %d models, want %d", len(models), len(want))
	}
	for _, model := range models {
		expected, ok := want[model.ID]
		if !ok {
			t.Fatalf("unexpected model %q", model.ID)
		}
		if len(model.Efforts) != len(expected) {
			t.Fatalf("%s efforts = %v, want %v", model.ID, model.Efforts, expected)
		}
		for i := range expected {
			if model.Efforts[i] != expected[i] {
				t.Fatalf("%s efforts = %v, want %v", model.ID, model.Efforts, expected)
			}
		}
	}
}

// A model that supports no effort must report none, so the UI renders no
// control at all rather than an empty or disabled dropdown.
func TestUnsupportedEffortIsEmptyNotPartial(t *testing.T) {
	if got := supportedEfforts(map[string]json.RawMessage{
		"supported": json.RawMessage(`false`),
		"low":       json.RawMessage(`{"supported":true}`),
	}); len(got) != 0 {
		t.Fatalf("efforts = %v, want none when the model does not support effort", got)
	}
	if got := supportedEfforts(nil); len(got) != 0 {
		t.Fatalf("efforts = %v, want none", got)
	}
}

// The API returns effort as an object, so order has to be imposed or the
// dropdown reshuffles between requests.
func TestEffortOrderIsStableAndAscending(t *testing.T) {
	raw := map[string]json.RawMessage{
		"supported": json.RawMessage(`true`),
		"max":       json.RawMessage(`{"supported":true}`),
		"low":       json.RawMessage(`{"supported":true}`),
		"xhigh":     json.RawMessage(`{"supported":true}`),
		"medium":    json.RawMessage(`{"supported":true}`),
		"high":      json.RawMessage(`{"supported":true}`),
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	for attempt := 0; attempt < 5; attempt++ {
		got := supportedEfforts(raw)
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("efforts = %v, want %v", got, want)
			}
		}
	}
}
