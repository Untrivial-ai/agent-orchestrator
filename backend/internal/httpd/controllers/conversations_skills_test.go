package controllers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func skillsRequest(t *testing.T, svc controllers.ConversationService) (int, []byte) {
	t.Helper()
	r := chi.NewRouter()
	(&controllers.ConversationsController{Svc: svc}).Register(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions/p1-1/conversation/skills", nil))
	return rec.Code, rec.Body.Bytes()
}

func decodeSkills(t *testing.T, body []byte) controllers.ConversationSkillsResponse {
	t.Helper()
	var out controllers.ConversationSkillsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func TestSkillsRouteReturnsTheProvidersList(t *testing.T) {
	status, body := skillsRequest(t, &fakeConversationService{
		skills: []ports.ChatSkill{
			{Name: "review", DisplayName: "Review", Description: "Look at the diff", InputHint: "<branch>", Source: "repo"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}
	got := decodeSkills(t, body)
	if len(got.Skills) != 1 {
		t.Fatalf("skills = %+v, want 1", got.Skills)
	}
	if got.Skills[0] != (controllers.ConversationSkillResponse{
		Name: "review", DisplayName: "Review", Description: "Look at the diff", InputHint: "<branch>", Source: "repo",
	}) {
		t.Errorf("skill = %+v", got.Skills[0])
	}
}

// A driver that cannot enumerate skills is not a failure. The composer keys `/` off
// an empty list, so an error here would leave the slash looking broken rather than
// simply unavailable.
func TestSkillsRouteAnswersEmptyWhenTheDriverCannotList(t *testing.T) {
	status, body := skillsRequest(t, &fakeConversationService{skillErr: chatsvc.ErrSkillsUnsupported})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}
	got := decodeSkills(t, body)
	if got.Skills == nil {
		t.Fatal("skills is null; an absent catalog must encode as [] so a client can iterate it")
	}
	if len(got.Skills) != 0 {
		t.Errorf("skills = %+v, want empty", got.Skills)
	}
}

// A session with no controller is a state to explain, not an empty menu to show.
func TestSkillsRouteReportsAMissingController(t *testing.T) {
	status, body := skillsRequest(t, &fakeConversationService{skillErr: chatsvc.ErrNoController})
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", status, body)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &envelope)
	if envelope.Code != "" && envelope.Code != "CHAT_CONTROLLER_NOT_READY" {
		t.Errorf("code = %q", envelope.Code)
	}
}

// A provider call that fails after the driver is confirmed to support skills
// (e.g. Codex app-server's `skills/list` returning a JSON-RPC error) must not
// fall through to a bare 500: that would hide a provider-side, usually
// transient fault behind "Internal server error" and give the client nothing
// to act on. It must answer a typed, non-500 status that carries the raw
// upstream error through for diagnostics.
func TestSkillsRouteAnswersATypedErrorWhenTheProviderCallFails(t *testing.T) {
	upstream := fmt.Errorf("%w: %w", chatsvc.ErrSkillsUnavailable,
		errors.New(`skills/list: rpc error: {"code":-32000,"message":"boom"}`))
	status, body := skillsRequest(t, &fakeConversationService{skillErr: upstream})
	if status == http.StatusInternalServerError {
		t.Fatalf("status = %d, want a typed non-500 (%s)", status, body)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", status, body)
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if envelope.Code != "CHAT_SKILLS_UNAVAILABLE" {
		t.Errorf("code = %q, want CHAT_SKILLS_UNAVAILABLE", envelope.Code)
	}
	if !strings.Contains(envelope.Message, "boom") {
		t.Errorf("message = %q, want it to preserve the raw upstream error", envelope.Message)
	}
}

// A nil service keeps the route registered and answers from the spec, so the
// OpenAPI contract and the router never disagree about which paths exist.
func TestSkillsRouteIsNotImplementedWithoutAService(t *testing.T) {
	status, _ := skillsRequest(t, nil)
	if status != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", status)
	}
}
