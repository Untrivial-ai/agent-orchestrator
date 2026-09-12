package controllers

import (
	"context"
	"errors"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimportsvc"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type searchAPIFake struct {
	err     error
	queries int
}

func (f *searchAPIFake) Discover(context.Context, sessionimport.DiscoverOptions, domain.ProjectID) ([]sessionimport.ImportableSession, error) {
	return nil, nil
}
func (f *searchAPIFake) Import(context.Context, domain.AgentHarness, string, domain.ProjectID) (domain.Session, bool, error) {
	return domain.Session{}, false, nil
}
func (f *searchAPIFake) Search(context.Context, string, int, string) (sessionimportsvc.SearchPage, error) {
	f.queries++
	return sessionimportsvc.SearchPage{Results: []sessionimportsvc.SearchResult{}, Status: sessionimportsvc.SearchStatus{Errors: []string{}}}, f.err
}
func (f *searchAPIFake) RefreshSearch() sessionimportsvc.SearchStatus {
	return sessionimportsvc.SearchStatus{Running: true, Errors: []string{}}
}
func (f *searchAPIFake) Destination(context.Context, string, string) (sessionimportsvc.Destination, error) {
	return sessionimportsvc.Destination{Action: "unavailable"}, f.err
}
func (f *searchAPIFake) ImportSelected(context.Context, string, sessionimportsvc.SelectedInput) (sessionimportsvc.SelectedResult, error) {
	return sessionimportsvc.SelectedResult{}, f.err
}
func TestSearchAPIValidationAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		err       error
		status    int
	}{
		{"limit", "/?limit=101", nil, 400}, {"query", "/", sessionimportsvc.ErrInvalidSearch, 400}, {"storage", "/", errors.New("database unavailable"), 500}, {"page", "/", nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &searchAPIFake{err: tc.err}
			c := &SessionsController{Import: f}
			w := httptest.NewRecorder()
			c.searchImports(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.name == "limit" && f.queries != 0 {
				t.Fatal("invalid query reached service")
			}
		})
	}
}
func TestSelectedAPISeparatesConfirmationAndRuntimeFailure(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{sessionimportsvc.ErrDestinationConfirmation, 422}, {errors.New("database unavailable"), 500}} {
		c := &SessionsController{Import: &searchAPIFake{err: tc.err}}
		w := httptest.NewRecorder()
		c.importSelected(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"confirmationToken":"x","addProject":true}`)))
		if w.Code != tc.status {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
