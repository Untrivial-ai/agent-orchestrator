package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cuesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cue"
)

// CueService is the controller-facing project cue contract.
type CueService interface {
	Create(ctx context.Context, projectID domain.ProjectID, input cuesvc.Input) (domain.Cue, error)
	Get(ctx context.Context, cueID domain.CueID) (domain.Cue, error)
	List(ctx context.Context, projectID domain.ProjectID) ([]domain.Cue, error)
	Update(ctx context.Context, cueID domain.CueID, input cuesvc.Input) (domain.Cue, error)
	Delete(ctx context.Context, cueID domain.CueID) error
	Invoke(ctx context.Context, cueID domain.CueID, sessionID domain.SessionID) (domain.SessionID, error)
}

// CuesController owns the project cue routes: reusable, user-defined quick
// actions scoped to a project.
type CuesController struct {
	Svc CueService
}

type invokeCueRequestBody struct {
	SessionID json.RawMessage `json:"sessionId"`
}

func (b *invokeCueRequestBody) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("invoke request body must be a JSON object")
	}
	type rawInvokeCueRequestBody invokeCueRequestBody
	return json.Unmarshal(data, (*rawInvokeCueRequestBody)(b))
}

// Register mounts the bounded cue REST routes.
func (c *CuesController) Register(r chi.Router) {
	r.Get("/projects/{projectId}/cues", c.list)
	r.Post("/projects/{projectId}/cues", c.create)
	r.Get("/cues/{cueId}", c.get)
	r.Patch("/cues/{cueId}", c.update)
	r.Delete("/cues/{cueId}", c.delete)
	r.Post("/cues/{cueId}/invoke", c.invoke)
}

func (c *CuesController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/projects/{projectId}/cues")
		return
	}
	cues, err := c.Svc.List(r.Context(), projectCueID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListCuesResponse{
		Cues: cueResponses(cues),
	})
}

func (c *CuesController) create(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/projects/{projectId}/cues")
		return
	}
	var req CreateCueRequest
	if !decodeCueBody(w, r, &req, 128<<10) {
		return
	}
	cue, err := c.Svc.Create(r.Context(), projectCueID(r), cueInput(UpdateCueRequest(req)))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, CueEnvelope{
		Cue: cueResponse(cue),
	})
}

func (c *CuesController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/cues/{cueId}")
		return
	}
	cueID, err := url.PathUnescape(chi.URLParam(r, "cueId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "CUE_ID_INVALID", "Invalid cue id", nil)
		return
	}
	cue, err := c.Svc.Get(r.Context(), domain.CueID(cueID))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, CueEnvelope{
		Cue: cueResponse(cue),
	})
}

func (c *CuesController) update(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/cues/{cueId}")
		return
	}
	var req UpdateCueRequest
	if !decodeCueBody(w, r, &req, 128<<10) {
		return
	}
	cueID, err := url.PathUnescape(chi.URLParam(r, "cueId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "CUE_ID_INVALID", "Invalid cue id", nil)
		return
	}
	cue, err := c.Svc.Update(r.Context(), domain.CueID(cueID), cueInput(req))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, CueEnvelope{
		Cue: cueResponse(cue),
	})
}

func (c *CuesController) delete(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/cues/{cueId}")
		return
	}
	cueID, err := url.PathUnescape(chi.URLParam(r, "cueId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "CUE_ID_INVALID", "Invalid cue id", nil)
		return
	}
	if err := c.Svc.Delete(r.Context(), domain.CueID(cueID)); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *CuesController) invoke(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/cues/{cueId}/invoke")
		return
	}
	var payload invokeCueRequestBody
	if !decodeCueBody(w, r, &payload, 4<<10) {
		return
	}
	var req InvokeCueRequest
	if payload.SessionID != nil {
		if err := json.Unmarshal(payload.SessionID, &req.SessionID); err != nil || strings.TrimSpace(req.SessionID) == "" {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_SESSION_ID", "Session id must not be blank when supplied", nil)
			return
		}
	}
	cueID, err := url.PathUnescape(chi.URLParam(r, "cueId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "CUE_ID_INVALID", "Invalid cue id", nil)
		return
	}
	sessionID, err := c.Svc.Invoke(r.Context(), domain.CueID(cueID), domain.SessionID(req.SessionID))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, InvokeCueResponse{
		SessionID: string(sessionID),
	})
}

// Bound allocation before decoding, and consume the entire body so trailing
// JSON or oversized whitespace cannot bypass validation. Empty bodies retain
// the existing default request behavior (notably project-dialog invocation).
func decodeCueBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := decoder.Decode(dst)
	if err == nil {
		var trailing any
		err = decoder.Decode(&trailing)
		if err == nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Expected a single JSON body", nil)
			return false
		}
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		envelope.WriteAPIError(w, r, http.StatusRequestEntityTooLarge, "bad_request", "CUE_BODY_TOO_LARGE", "Cue request body is too large", nil)
	} else {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
	}
	return false
}

func projectCueID(r *http.Request) domain.ProjectID {
	return domain.ProjectID(chi.URLParam(r, "projectId"))
}

func cueInput(req UpdateCueRequest) cuesvc.Input {
	return cuesvc.Input{
		Name:        req.Name,
		Description: req.Description,
		Type:        domain.CueType(req.Type),
		Command:     req.Command,
		Prompt:      req.Prompt,
	}
}

func cueResponses(in []domain.Cue) []CueResponse {
	out := make([]CueResponse, 0, len(in))
	for _, cue := range in {
		out = append(out, cueResponse(cue))
	}
	return out
}

func cueResponse(cue domain.Cue) CueResponse {
	return CueResponse{
		ID:          string(cue.ID),
		ProjectID:   string(cue.ProjectID),
		Name:        cue.Name,
		Description: cue.Description,
		Type:        string(cue.Type),
		Command:     cue.Command,
		Prompt:      cue.Prompt,
		CreatedAt:   cue.CreatedAt,
		UpdatedAt:   cue.UpdatedAt,
	}
}
