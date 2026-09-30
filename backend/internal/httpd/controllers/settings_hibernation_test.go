package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	settingssvc "github.com/aoagents/agent-orchestrator/backend/internal/service/settings"
)

type hibernationSettingsService struct {
	SettingsService
	enabled bool
	calls   int
}

func (s *hibernationSettingsService) SetChatHibernationEnabled(_ context.Context, enabled bool) (settingssvc.Snapshot, error) {
	s.enabled = enabled
	s.calls++
	return settingssvc.Snapshot{ChatHibernationEnabled: enabled}, nil
}
func (s *hibernationSettingsService) ChatHarnesses([]domain.AgentHarness) []domain.AgentHarness {
	return nil
}
func (s *hibernationSettingsService) Offering() settingssvc.Offering { return settingssvc.Offering{} }

func TestSettingsChatHibernationRequiresBoolean(t *testing.T) {
	svc := &hibernationSettingsService{}
	c := &SettingsController{Svc: svc}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"true"}`} {
		w := httptest.NewRecorder()
		c.setChatHibernation(w, httptest.NewRequest(http.MethodPatch, "/api/v1/settings/chat-hibernation", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || svc.calls != 0 {
			t.Fatalf("body=%s status=%d calls=%d", body, w.Code, svc.calls)
		}
	}
	for _, enabled := range []bool{true, false} {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		w := httptest.NewRecorder()
		c.setChatHibernation(w, httptest.NewRequest(http.MethodPatch, "/api/v1/settings/chat-hibernation", strings.NewReader(body)))
		var got SettingsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.ChatHibernationEnabled != enabled {
			t.Fatalf("body=%s status=%d response=%+v err=%v", body, w.Code, got, err)
		}
	}
}
