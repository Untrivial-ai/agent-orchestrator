package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestExchangeOAuthCodeOmitsEmptyPKCEVerifier(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode OAuth request: %v", err)
		}
		if _, exists := payload["code_verifier"]; exists {
			t.Error("code_verifier must be omitted for GitHub's installation authorization callback")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"access_token":"github-token"}`)),
		}, nil
	})}

	client := &Client{
		clientID:     "client-id",
		clientSecret: "client-secret",
		publicURL:    "https://cloud.example.test",
		webBaseURL:   "https://github.example.test",
		httpClient:   httpClient,
	}

	token, err := client.ExchangeOAuthCode(context.Background(), "oauth-code", "")
	if err != nil {
		t.Fatalf("exchange OAuth code: %v", err)
	}
	if token != "github-token" {
		t.Fatalf("token = %q, want github-token", token)
	}
}
