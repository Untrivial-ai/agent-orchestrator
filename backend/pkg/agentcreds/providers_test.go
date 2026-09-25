package agentcreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"}]}`))
			}))
			defer server.Close()

			cred := tc.cred
			cred.BaseURL = server.URL
			New(server.Client()).Validate(context.Background(), cred)
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

func TestAnthropicModelDiscoveryFollowsPagination(t *testing.T) {
	var afterIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "1000" {
			t.Fatalf("limit = %q, want 1000", r.URL.Query().Get("limit"))
		}
		after := r.URL.Query().Get("after_id")
		afterIDs = append(afterIDs, after)
		if after == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-first"}],"has_more":true,"last_id":"model-1"}`))
			return
		}
		if after != "model-1" {
			t.Fatalf("after_id = %q, want model-1", after)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-second"}],"has_more":false,"last_id":"model-2"}`))
	}))
	defer server.Close()

	result := New(server.Client()).Validate(context.Background(), Credential{
		Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty, BaseURL: server.URL,
	})
	if result.State != StateValid || len(result.Models) != 2 || len(afterIDs) != 2 {
		t.Fatalf("result = state %q models %v requests %v", result.State, result.Models, afterIDs)
	}
}

func TestAnthropicModelDiscoveryRejectsRepeatedCursor(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-first"}],"has_more":true,"last_id":"same"}`))
	}))
	defer server.Close()
	result := New(server.Client()).Validate(context.Background(), Credential{
		Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty, BaseURL: server.URL,
	})
	if result.State != StateUnknown || requests != 2 {
		t.Fatalf("state/requests = %q/%d, want unknown after two requests", result.State, requests)
	}
}

// A gateway need not implement model listing, so an empty list there is not a
// verdict either way, and a 404 is Unknown rather than a rejection.
func TestGatewayToleratesAMissingModelEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	result := New(server.Client()).Validate(context.Background(), Credential{
		Kind: KindAPIKey, Secret: "k", Provider: ProviderGateway, BaseURL: server.URL,
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

func TestGatewayCatalogPreservesArbitraryNonEmptyModelIDs(t *testing.T) {
	body := `{"data":[
		{"id":"kimi-for-coding"},
		{"id":"kimi-for-coding-highspeed"},
		{"id":"k3"},
		{"id":"k3-256k"},
		{"id":"glm-4.6"},
		{"id":""}
	]}`
	wantGateway := []string{
		"kimi-for-coding",
		"kimi-for-coding-highspeed",
		"k3",
		"k3-256k",
		"glm-4.6",
	}

	tests := []struct {
		name       string
		credential Credential
		wantState  State
		wantIDs    []string
	}{
		{
			name:       "gateway",
			credential: Credential{Kind: KindAPIKey, Secret: "k", Provider: ProviderGateway},
			wantState:  StateValid,
			wantIDs:    wantGateway,
		},
		{
			name:       "first party",
			credential: Credential{Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty},
			wantState:  StateValid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			credential := tc.credential
			credential.BaseURL = server.URL
			result := New(server.Client()).Validate(context.Background(), credential)
			if result.State != tc.wantState {
				t.Fatalf("state = %q (%s), want %q", result.State, result.Detail, tc.wantState)
			}
			if len(result.Models) != len(tc.wantIDs) {
				t.Fatalf("models = %v, want ids %v", result.Models, tc.wantIDs)
			}
			for index, wantID := range tc.wantIDs {
				if result.Models[index].ID != wantID {
					t.Fatalf("model %d id = %q, want %q", index, result.Models[index].ID, wantID)
				}
			}
		})
	}
}

func TestGatewayRequestPreservesBasePathAndUsesOnlyAPIKeyHeader(t *testing.T) {
	spec, err := New(nil).requestFor(context.Background(), ProviderGateway, Credential{
		Kind: KindAPIKey, Secret: "kimi-key", Provider: ProviderGateway,
		BaseURL: "https://api.kimi.com/coding/",
	})
	if err != nil {
		t.Fatal(err)
	}

	request := spec.request
	if target := request.URL.Scheme + "://" + request.URL.Host + request.URL.Path; target != "https://api.kimi.com/coding/v1/models" {
		t.Fatalf("target = %q, want %q", target, "https://api.kimi.com/coding/v1/models")
	}
	if request.URL.Query().Get("limit") != "1000" {
		t.Fatalf("limit = %q, want 1000", request.URL.Query().Get("limit"))
	}
	if got := request.Header.Get("x-api-key"); got != "kimi-key" {
		t.Fatalf("x-api-key = %q, want %q", got, "kimi-key")
	}
	if got := request.Header.Get("authorization"); got != "" {
		t.Fatalf("authorization = %q, want empty", got)
	}
}

// The validating call also supplies the first-party catalog.
func TestValidationReturnsThatProvidersModelIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-4-5-20251101"},{"id":"gpt-4o"}]}`))
	}))
	defer server.Close()

	result := New(server.Client()).Validate(context.Background(), Credential{
		Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty, BaseURL: server.URL,
	})
	if result.State != StateValid {
		t.Fatalf("state = %q (%s), want %q", result.State, result.Detail, StateValid)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "claude-opus-4-5-20251101" {
		t.Fatalf("models = %v, want the Claude model only", result.Models)
	}
}

// A credential kind that cannot authenticate to a provider is a programming
// error, and must surface as Unknown rather than as a bogus rejection.
func TestMismatchedKindAndProviderIsUnknown(t *testing.T) {
	result := New(nil).Validate(context.Background(), Credential{
		Kind: Kind("unsupported"), Secret: "token", Provider: ProviderFirstParty,
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
