package controllers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimportsvc"
)

// SessionSearchService exposes cached search, destination preview, and selected import.
type SessionSearchService interface {
	Search(context.Context, string, int, string) (sessionimportsvc.SearchPage, error)
	RefreshSearch() sessionimportsvc.SearchStatus
	Destination(context.Context, string, string) (sessionimportsvc.Destination, error)
	ImportSelected(context.Context, string, sessionimportsvc.SelectedInput) (sessionimportsvc.SelectedResult, error)
}

func (c *SessionsController) searchService(w http.ResponseWriter, r *http.Request) (SessionSearchService, bool) {
	s, ok := c.Import.(SessionSearchService)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotImplemented, "not_implemented", "SEARCH_UNAVAILABLE", "Session search is unavailable", nil)
	}
	return s, ok
}
func (c *SessionsController) searchImports(w http.ResponseWriter, r *http.Request) {
	s, ok := c.searchService(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_QUERY", "limit must be between 1 and 100", nil)
			return
		}
	}
	page, err := s.Search(r.Context(), r.URL.Query().Get("query"), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		if errors.Is(err, sessionimportsvc.ErrInvalidSearch) {
			envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_QUERY", err.Error(), nil)
		} else {
			envelope.WriteError(w, r, err)
		}
		return
	}
	envelope.WriteJSON(w, 200, page)
}
func (c *SessionsController) refreshImports(w http.ResponseWriter, r *http.Request) {
	s, ok := c.searchService(w, r)
	if !ok {
		return
	}
	envelope.WriteJSON(w, 200, s.RefreshSearch())
}
func (c *SessionsController) importDestination(w http.ResponseWriter, r *http.Request) {
	s, ok := c.searchService(w, r)
	if !ok {
		return
	}
	d, err := s.Destination(r.Context(), chi.URLParam(r, "resultId"), r.URL.Query().Get("locateFolder"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, 200, d)
}
func (c *SessionsController) importSelected(w http.ResponseWriter, r *http.Request) {
	s, ok := c.searchService(w, r)
	if !ok {
		return
	}
	var req sessionimportsvc.SelectedInput
	if err := decodeJSON(r, &req); err != nil {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := s.ImportSelected(r.Context(), chi.URLParam(r, "resultId"), req)
	if err != nil {
		if errors.Is(err, sessionimportsvc.ErrDestinationConfirmation) || errors.Is(err, sessionimportsvc.ErrImportSessionNotFound) || errors.Is(err, os.ErrNotExist) {
			envelope.WriteAPIError(w, r, 422, "unprocessable_entity", "IMPORT_UNAVAILABLE", err.Error(), nil)
		} else {
			envelope.WriteError(w, r, err)
		}
		return
	}
	envelope.WriteJSON(w, 200, result)
}
