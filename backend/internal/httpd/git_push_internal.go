package httpd

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/gitservice"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

const gitPushCapabilityHeader = "X-AO-Git-Push-Capability"

type approveGitPushRequest struct {
	ApprovalID string `json:"approvalId"`
	ApprovedBy string `json:"approvedBy"`
}

type revokeGitPushRequest struct {
	ApprovalID  string `json:"approvalId"`
	RequestedBy string `json:"requestedBy"`
}

func mountTrustedGitPush(r chi.Router, svc *gitservice.Service, capability string) {
	if svc == nil || capability == "" {
		return
	}
	trusted := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			provided := req.Header.Get(gitPushCapabilityHeader)
			if !localControlRequest(req) || len(provided) != len(capability) || subtle.ConstantTimeCompare([]byte(provided), []byte(capability)) != 1 {
				envelope.WriteAPIError(w, req, http.StatusForbidden, "forbidden", "GIT_PUSH_CAPABILITY_REQUIRED", "trusted desktop capability required", nil)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
	r.Route("/internal/git-push", func(r chi.Router) {
		r.Use(trusted)
		r.Post("/prepare", func(w http.ResponseWriter, req *http.Request) {
			var body gitservice.PrepareInput
			if !decodeStrictJSON(w, req, &body) {
				return
			}
			proposal, err := svc.Prepare(req.Context(), body)
			if err != nil {
				writeGitPushError(w, req, err)
				return
			}
			envelope.WriteJSON(w, http.StatusCreated, proposal)
		})
		r.Post("/approve-and-push", func(w http.ResponseWriter, req *http.Request) {
			var body approveGitPushRequest
			if !decodeStrictJSON(w, req, &body) {
				return
			}
			result, err := svc.ApproveAndPush(req.Context(), strings.TrimSpace(body.ApprovalID), body.ApprovedBy)
			if err != nil {
				writeGitPushError(w, req, err)
				return
			}
			envelope.WriteJSON(w, http.StatusOK, result)
		})
		r.Post("/revoke", func(w http.ResponseWriter, req *http.Request) {
			var body revokeGitPushRequest
			if !decodeStrictJSON(w, req, &body) {
				return
			}
			if err := svc.Revoke(req.Context(), strings.TrimSpace(body.ApprovalID), body.RequestedBy); err != nil {
				writeGitPushError(w, req, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})
}

func decodeStrictJSON(w http.ResponseWriter, req *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		envelope.WriteAPIError(w, req, http.StatusBadRequest, "bad_request", "INVALID_JSON", "request body must be valid JSON", nil)
		return false
	}
	return true
}

func writeGitPushError(w http.ResponseWriter, req *http.Request, err error) {
	status, code := http.StatusBadRequest, "GIT_PUSH_REJECTED"
	if errors.Is(err, gitservice.ErrNotFound) {
		status, code = http.StatusNotFound, "PUSH_APPROVAL_NOT_FOUND"
	}
	if errors.Is(err, gitservice.ErrNotConsumable) || errors.Is(err, gitservice.ErrNotApprovable) {
		status, code = http.StatusConflict, "PUSH_APPROVAL_NOT_VALID"
	}
	if errors.Is(err, gitservice.ErrRepositoryRace) {
		status, code = http.StatusConflict, "PUSH_APPROVAL_STATE_MISMATCH"
	}
	envelope.WriteAPIError(w, req, status, "git_push_rejected", code, err.Error(), nil)
}
