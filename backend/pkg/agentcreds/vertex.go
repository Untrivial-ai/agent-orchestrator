package agentcreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// vertexScope is the OAuth scope a Vertex probe needs.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

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
	case KindGoogleServiceAccount:
		// A service-account key is not a bearer token. It must be signed into
		// a JWT and exchanged for one before anything can be probed.
		exchanged, err := v.googleAccessTokenFromServiceAccount(ctx, cred.Secret)
		if err != nil {
			return requestSpec{}, err
		}
		token = exchanged
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

// googleAccessTokenFromServiceAccount delegates service-account parsing,
// signing, and exchange to the official OAuth implementation. Credential-chain
// file types remain delegated to gcloud by the resolver.
func (v *Validator) googleAccessTokenFromServiceAccount(ctx context.Context, keyJSON string) (string, error) {
	config, err := google.JWTConfigFromJSON([]byte(keyJSON), vertexScope)
	if err != nil {
		return "", fmt.Errorf("agentcreds: parse service account key: %w", err)
	}
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, v.client)
	token, err := config.TokenSource(tokenCtx).Token()
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.Response != nil &&
			(retrieveErr.Response.StatusCode == http.StatusUnauthorized || retrieveErr.Response.StatusCode == http.StatusBadRequest) {
			return "", fmt.Errorf("%w: Google rejected the service account key: %s",
				ErrInvalidCredential, providerErrorMessage(retrieveErr.Body))
		}
		return "", fmt.Errorf("agentcreds: exchange service account key: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", fmt.Errorf("agentcreds: token exchange returned no access token")
	}
	return token.AccessToken, nil
}
