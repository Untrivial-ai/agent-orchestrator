package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientWaitForWorkHitsEndpoint(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	if err := c.WaitForWork(context.Background()); err != nil {
		t.Fatalf("WaitForWork: %v", err)
	}
	if method != http.MethodGet || path != "/worker/work/wait" {
		t.Fatalf("called %s %s, want GET /worker/work/wait", method, path)
	}
}

// An older control plane without the endpoint (404) surfaces as an error so the
// supervisor falls back to a bounded back-off instead of treating it as "no work".
func TestClientWaitForWorkErrorsWhenUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	if err := c.WaitForWork(context.Background()); err == nil {
		t.Fatal("expected an error when the control plane lacks the wait endpoint")
	}
}
