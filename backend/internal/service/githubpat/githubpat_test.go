package githubpat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func githubClient(status int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{"message":"test response"}`)),
			Header:     make(http.Header),
		}, nil
	})}
}

func TestListReposKeepsTokenOnUnauthorized(t *testing.T) {
	service := newWithClient(t.TempDir(), githubClient(http.StatusUnauthorized), "https://github.example")
	if err := service.StorePAT(context.Background(), "rejected-token"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}

	_, err := service.ListRepos(context.Background())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ListRepos error = %v, want ErrInvalidCredentials", err)
	}
	// A 401 must NOT delete the stored token. A transient GitHub 401 would
	// otherwise permanently wipe a still-valid credential and force a full
	// reconnect. The token is preserved until the user reconnects (overwrite)
	// or explicitly disconnects (DeletePAT).
	if !service.HasPAT(context.Background()) {
		t.Fatal("HasPAT = false: token was deleted on a 401 (it must be preserved)")
	}
}

func TestListReposDoesNotRemoveTokenForTransientFailure(t *testing.T) {
	service := newWithClient(t.TempDir(), githubClient(http.StatusInternalServerError), "https://github.example")
	if err := service.StorePAT(context.Background(), "still-valid-token"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}

	if _, err := service.ListRepos(context.Background()); err == nil {
		t.Fatal("ListRepos error = nil, want failure")
	}
	if !service.HasPAT(context.Background()) {
		t.Fatal("HasPAT = false after a transient GitHub failure")
	}
}
