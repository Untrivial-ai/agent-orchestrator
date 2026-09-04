package controllers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func configOptionsRequest(
	t *testing.T,
	method string,
	body string,
	svc controllers.ConversationService,
) (int, []byte) {
	t.Helper()
	r := chi.NewRouter()
	(&controllers.ConversationsController{Svc: svc}).Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		method,
		"/sessions/p1-1/conversation/config-options/model",
		bytes.NewBufferString(body),
	))
	return rec.Code, rec.Body.Bytes()
}

func TestConfigOptionsRoutePreservesProviderCatalog(t *testing.T) {
	current := true
	svc := &fakeConversationService{configOptions: []ports.ChatConfigOption{
		{
			ID: "model", Name: "Model", Category: "model", Type: ports.ChatConfigOptionSelect,
			Current: ports.ChatConfigOptionValue{Select: "opus"},
			Choices: []ports.ChatConfigOptionChoice{{
				Value: "opus", Name: "Opus", Description: "Most capable",
				Group: "claude", GroupName: "Claude models",
			}},
		},
		{ID: "fast", Name: "Fast mode", Type: ports.ChatConfigOptionBoolean,
			Current: ports.ChatConfigOptionValue{Boolean: &current}},
	}}

	r := chi.NewRouter()
	(&controllers.ConversationsController{Svc: svc}).Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/sessions/p1-1/conversation/config-options", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.Bytes())
	}
	var got controllers.ConversationConfigOptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Options) != 2 || got.Options[0].CurrentValue != "opus" {
		t.Fatalf("options = %#v", got.Options)
	}
	if len(got.Options[0].Choices) != 1 || got.Options[0].Choices[0].GroupName != "Claude models" {
		t.Fatalf("grouped choice = %#v", got.Options[0].Choices)
	}
	if got.Options[1].CurrentBoolean == nil || !*got.Options[1].CurrentBoolean {
		t.Fatalf("boolean option = %#v", got.Options[1])
	}
}

func TestConfigOptionsRouteSerializesEmptyChoices(t *testing.T) {
	svc := &fakeConversationService{configOptions: []ports.ChatConfigOption{{
		ID: "model", Name: "Model", Category: "model", Type: ports.ChatConfigOptionSelect,
	}}}
	r := chi.NewRouter()
	(&controllers.ConversationsController{Svc: svc}).Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/sessions/p1-1/conversation/config-options", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.Bytes())
	}

	var got struct {
		Options []struct {
			Choices json.RawMessage `json:"choices"`
		} `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Options) != 1 || string(got.Options[0].Choices) != "[]" {
		t.Fatalf("choices = %s, want [] (body = %s)", got.Options[0].Choices, rec.Body.Bytes())
	}
}

func TestSetConfigOptionAcceptsSelectAndBooleanValues(t *testing.T) {
	svc := &fakeConversationService{configOptions: []ports.ChatConfigOption{}}
	status, body := configOptionsRequest(t, http.MethodPatch, `{"value":"opus"}`, svc)
	if status != http.StatusOK {
		t.Fatalf("select status = %d, body = %s", status, body)
	}
	if svc.setConfigID != "model" || svc.setConfigValue.Select != "opus" {
		t.Fatalf("select request = %q %#v", svc.setConfigID, svc.setConfigValue)
	}

	status, body = configOptionsRequest(t, http.MethodPatch, `{"enabled":false}`, svc)
	if status != http.StatusOK {
		t.Fatalf("boolean status = %d, body = %s", status, body)
	}
	if svc.setConfigValue.Boolean == nil || *svc.setConfigValue.Boolean {
		t.Fatalf("boolean request = %#v", svc.setConfigValue)
	}
}

func TestSetConfigOptionRequiresExactlyOneValueShape(t *testing.T) {
	for _, body := range []string{`{}`, `{"value":"opus","enabled":true}`} {
		status, response := configOptionsRequest(t, http.MethodPatch, body, &fakeConversationService{})
		if status != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400 (%s)", body, status, response)
		}
	}
}
