package local

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionQueriesUseFixedRoutesAndRecheckIdentity(t *testing.T) {
	for _, resource := range []domain.TestDaemonResource{domain.TestDaemonReviews, domain.TestDaemonConversation} {
		t.Run(string(resource), func(t *testing.T) {
			f := fixture(t)
			transport := f.a.ops.client.Transport
			count := 0
			route := "/api/v1/sessions/worker-42/" + string(resource)
			f.a.ops.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == route {
					count++
				}
				return transport.RoundTrip(r)
			})
			request := domain.TestDaemonQueryRequest{Resource: resource, SessionID: "worker-42"}
			result, err := f.a.QueryDaemon(context.Background(), f.s.target, request)
			if err != nil || !json.Valid(result.Data) || count != 1 {
				t.Fatalf("query count=%d err=%v", count, err)
			}
			f.a.ops.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				if r.URL.Path == route {
					f.times[11] = f.times[11].Add(1)
				}
				return response, err
			})
			if _, err := f.a.QueryDaemon(context.Background(), f.s.target, request); err == nil {
				t.Fatal("query accepted changed process birth")
			}
		})
	}
}

func TestSessionQueriesRejectInvalidSelectorsBeforeHTTP(t *testing.T) {
	for _, request := range []domain.TestDaemonQueryRequest{
		{Resource: domain.TestDaemonReviews},
		{Resource: domain.TestDaemonReviews, SessionID: "../other"},
		{Resource: domain.TestDaemonConversation, SessionID: "worker%2fother"},
		{Resource: domain.TestDaemonConversation, SessionID: "worker?query"},
		{Resource: domain.TestDaemonProjects, SessionID: "worker"},
	} {
		f := fixture(t)
		f.a.ops.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("invalid selector reached HTTP"); return nil, nil })
		if _, err := f.a.QueryDaemon(context.Background(), f.s.target, request); err == nil {
			t.Fatalf("accepted %+v", request)
		}
	}
}
