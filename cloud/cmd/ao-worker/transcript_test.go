package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenCodeResolverLocateIsNoop(t *testing.T) {
	r := transcriptResolver{harness: "opencode", workspace: "/workspace", dataDir: t.TempDir()}
	if _, _, ok := r.locate(); ok {
		t.Fatal("opencode locate() should be a no-op until transcript capture is implemented")
	}
	if _, err := r.rehydratePath("id"); err == nil {
		t.Fatal("opencode rehydratePath should report unsupported")
	}
}

func TestGetTranscript404IsNothingCaptured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	_, ok, err := c.getTranscript(context.Background())
	if err != nil {
		t.Fatalf("getTranscript: %v", err)
	}
	if ok {
		t.Fatal("404 must report nothing captured, not a hit")
	}
}

func TestGetTranscript200Decodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != transcriptPath {
			t.Errorf("called %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentSessionId":"a1","harness":"opencode","transcript":"eyJ4IjoxfQ==","preservedGitRef":"refs/ao/preserved/s1"}`))
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	got, ok, err := c.getTranscript(context.Background())
	if err != nil || !ok {
		t.Fatalf("getTranscript ok=%v err=%v", ok, err)
	}
	if got.AgentSessionID != "a1" || got.Harness != "opencode" || got.PreservedGitRef != "refs/ao/preserved/s1" {
		t.Fatalf("decoded wrong checkpoint: %+v", got)
	}
}

func TestPutTranscriptSendsPut(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	if err := c.putTranscript(context.Background(), transcriptCheckpoint{AgentSessionID: "a1"}); err != nil {
		t.Fatalf("putTranscript: %v", err)
	}
	if method != http.MethodPut || path != transcriptPath {
		t.Fatalf("called %s %s, want PUT %s", method, path, transcriptPath)
	}
}