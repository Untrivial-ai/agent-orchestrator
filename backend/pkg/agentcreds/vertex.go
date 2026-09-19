package agentcreds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// vertexRequest builds the Vertex AI probe.
//
// Vertex publishes Claude through Anthropic's Model Garden publisher namespace.
// The regional host selects the location, while x-goog-user-project carries the
// quota/billing project. The listing returns Vertex-format IDs such as
// claude-opus-4-5@20251101.
func (v *Validator) vertexRequest(ctx context.Context, cred Credential) (requestSpec, error) {
	region := strings.TrimSpace(cred.Region)
	project := strings.TrimSpace(cred.Project)
	if region == "" || project == "" {
		return requestSpec{}, fmt.Errorf("agentcreds: Vertex needs a project and a region")
	}

	token := strings.TrimSpace(cred.Secret)
	switch cred.Kind {
	case KindGoogleAccessToken:
		// GOOGLE_OAUTH_ACCESS_TOKEN: already an access token.
	default:
		return requestSpec{}, fmt.Errorf("agentcreds: credential kind %q cannot authenticate to Vertex", cred.Kind)
	}
	if token == "" {
		return requestSpec{}, fmt.Errorf("agentcreds: no Vertex access token from %s", cred.Source)
	}

	base := firstNonEmpty(cred.BaseURL, fmt.Sprintf("https://%s-aiplatform.googleapis.com", region))
	endpoint := strings.TrimRight(base, "/") + "/v1beta1/publishers/anthropic/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return requestSpec{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("x-goog-user-project", project)
	return requestSpec{
		request: request, parseModels: parseVertexModels,
		// Authenticating to Google says nothing about Claude entitlement on
		// this project, so an empty publisher list is not a pass.
		requireModels: true, label: "Vertex AI",
	}, nil
}

// parseVertexModels reads the publisher-model list.
func parseVertexModels(body []byte) ([]Model, error) {
	var payload struct {
		PublisherModels []struct {
			Name      string `json:"name"`
			VersionID string `json:"versionId"`
		} `json:"publisherModels"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(payload.PublisherModels)+len(payload.Models))
	appendModel := func(name string) {
		// Names arrive fully qualified: publishers/anthropic/models/claude-…
		if index := strings.LastIndex(name, "/"); index >= 0 {
			name = name[index+1:]
		}
		if isClaudeModelID(name) {
			// Vertex's publisher listing carries no reasoning levels either.
			models = append(models, Model{ID: name})
		}
	}
	for _, model := range payload.PublisherModels {
		appendModel(model.Name)
	}
	for _, model := range payload.Models {
		appendModel(model.Name)
	}
	return models, nil
}
