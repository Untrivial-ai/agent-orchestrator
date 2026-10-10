package host

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	proxycore "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Boundary runs before every SDK route: the daemon's key opens /ao and the management allowlist, a session ticket opens inference.
type Boundary struct {
	Routes                   *Routes
	ControlKey, InferenceKey string
	Logins                   *Logins
	Activity                 *Activity
}
type request struct {
	ID       string           `json:"id"`
	AuthID   string           `json:"auth_id"`
	Provider string           `json:"provider"`
	Routes   []Route          `json:"routes"`
	AuthIDs  []string         `json:"auth_ids"`
	Method   string           `json:"method"`
	URL      string           `json:"url"`
	Body     *json.RawMessage `json:"body"`
	APIKey   string           `json:"api_key"`
	BaseURL  string           `json:"base_url"`
	Label    string           `json:"label"`
}

var management = map[string]bool{
	"GET /v8/management/credentials": true, "POST /v8/management/credentials": true, "DELETE /v8/management/credentials": true,
	"GET /v8/management/oauth/auth-url": true, "GET /v8/management/oauth/status": true, "POST /v8/management/oauth/callback": true, "DELETE /v8/management/oauth/session": true,
	"GET /v0/management/codex-api-key": true, "PUT /v0/management/codex-api-key": true, "DELETE /v0/management/codex-api-key": true,
	"GET /v0/management/claude-api-key": true, "PUT /v0/management/claude-api-key": true, "DELETE /v0/management/claude-api-key": true,
}

// sessionPaths names the provider whose sessions may call each path; any session may list its models.
var sessionPaths = map[string]string{"/v1/responses": "codex", "/v1/responses/compact": "codex", "/v1/messages": "claude", "/v1/messages/count_tokens": "claude", "/v1/models": ""}

func (b Boundary) Middleware(c *gin.Context) {
	r, path := c.Request, c.Request.URL.Path
	r.Header.Del(accountHeader)
	r.Header.Del(providerHeader)
	own := strings.HasPrefix(path, "/ao/")
	if own || strings.HasPrefix(path, "/v8/management/") || strings.HasPrefix(path, "/v0/management/") {
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+b.ControlKey)) != 1 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if !own && !management[r.Method+" "+path] {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		// The SDK's OAuth forwarder binds all interfaces; AO runs its own loopback relay.
		query := r.URL.Query()
		query.Del("is_webui")
		r.URL.RawQuery = query.Encode()
		c.Next()
		return
	}
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		c.AbortWithStatus(http.StatusNotImplemented)
		return
	}
	provider, known := sessionPaths[path]
	if !known {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	route, release, ok := b.Routes.Acquire(cmp.Or(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), c.GetHeader("X-Api-Key")))
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "Session account unavailable. Please sign in.", "type": "authentication_error"}})
		return
	}
	defer release()
	if provider != "" && provider != route.Provider {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	r.Header.Set(accountHeader, route.AuthID)
	r.Header.Set(providerHeader, route.Provider)
	r.Header.Set("Authorization", "Bearer "+b.InferenceKey)
	r.Header.Del("X-Api-Key")
	c.Set(sessionKey, route.TicketHash)
	// Codex's own catalogue request (client_version) has a format of its own and is left to the SDK.
	if path == "/v1/models" && r.Method == http.MethodGet && !r.URL.Query().Has("client_version") {
		serveSessionModels(c, route.AuthID)
		return
	}
	c.Next()
}

// call adapts an /ao handler: it reads the JSON body of a write, and answers with the returned status and JSON body.
func call(handle func(context.Context, request) (int, any)) gin.HandlerFunc {
	return func(c *gin.Context) {
		body := request{ID: cmp.Or(c.Param("id"), c.Query("id"))}
		status, out := http.StatusBadRequest, any(nil)
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodDelete || c.ShouldBindJSON(&body) == nil {
			status, out = handle(c.Request.Context(), body)
		}
		if c.Status(status); out != nil {
			c.JSON(status, out)
		}
	}
}
func (b Boundary) Configure(engine *gin.Engine, h *handlers.BaseAPIHandler, _ *config.Config) {
	m := h.AuthManager
	m.SetSelector(exactSelector{})
	b.Logins.auth = m
	forAccount := func(handle func(context.Context, *coreauth.Auth, request) (int, any)) gin.HandlerFunc {
		return call(func(ctx context.Context, body request) (int, any) {
			auth, ok := m.GetByID(body.AuthID)
			if !ok || auth.Provider != body.Provider {
				return http.StatusNotFound, nil
			}
			return handle(ctx, auth, body)
		})
	}
	engine.GET("/ao/status", call(func(context.Context, request) (int, any) { return http.StatusOK, gin.H{"protocol_version": 3} }))
	engine.PUT("/ao/routes", call(func(_ context.Context, body request) (int, any) {
		if err := b.Routes.Apply(body.Routes, body.AuthIDs); errors.Is(err, ErrBusy) {
			return http.StatusConflict, gin.H{"code": "SESSION_BUSY"}
		} else if err != nil {
			return http.StatusInternalServerError, nil
		}
		return http.StatusNoContent, nil
	}))
	engine.POST("/ao/account-models", forAccount(func(_ context.Context, auth *coreauth.Auth, _ request) (int, any) {
		models := []gin.H{}
		for _, model := range proxycore.GlobalModelRegistry().GetModelsForClient(auth.ID) {
			entry := gin.H{"id": model.ID, "label": cmp.Or(strings.TrimSpace(model.DisplayName), model.ID), "provider": model.Type}
			if model.Thinking != nil {
				entry["efforts"] = model.Thinking.Levels
			}
			models = append(models, entry)
		}
		if len(models) == 0 {
			return http.StatusServiceUnavailable, nil
		}
		return http.StatusOK, gin.H{"models": models}
	}))
	engine.POST("/ao/account-state", forAccount(func(_ context.Context, auth *coreauth.Auth, _ request) (int, any) {
		return http.StatusOK, b.Activity.report(accountState(auth, time.Now()), auth.ID, time.Now())
	}))
	engine.POST("/ao/provider-call", forAccount(func(ctx context.Context, auth *coreauth.Auth, body request) (int, any) {
		if origin := providerOrigins[auth.Provider]; origin == "" || !strings.HasPrefix(body.URL, origin) {
			return http.StatusBadRequest, nil
		}
		status, data, err := providerCall(ctx, m, auth, body.Method, body.URL, body.Body)
		// A refused sign-in is renewed and the call repeated once; the SDK records on the account how the renewal went.
		if err == nil && status == http.StatusUnauthorized && auth.Attributes["api_key"] == "" {
			if auth, err = m.ForceRefreshAuth(ctx, auth.ID); err == nil {
				status, data, err = providerCall(ctx, m, auth, body.Method, body.URL, body.Body)
			}
		}
		if err != nil {
			return http.StatusBadGateway, nil
		}
		if !json.Valid(data) {
			data, _ = json.Marshal(string(data))
		}
		return http.StatusOK, gin.H{"status": status, "body": json.RawMessage(data)}
	}))
	engine.POST("/ao/account-resume", forAccount(func(ctx context.Context, auth *coreauth.Auth, _ request) (int, any) {
		if _, _, err := m.ResetQuota(ctx, auth.ID); err != nil {
			return http.StatusInternalServerError, nil
		}
		return http.StatusNoContent, nil
	}))
	engine.POST("/ao/account-refresh", forAccount(func(ctx context.Context, auth *coreauth.Auth, _ request) (int, any) {
		if auth.Attributes["api_key"] != "" {
			return http.StatusBadRequest, nil
		}
		if _, err := m.ForceRefreshAuth(ctx, auth.ID); err != nil {
			return http.StatusBadGateway, nil
		}
		return http.StatusNoContent, nil
	}))
	engine.GET("/ao/login-result/:id", call(func(ctx context.Context, body request) (int, any) {
		for _, a := range m.List() {
			if a.Metadata["ao_login_id"] != body.ID && a.FileName != "ao-"+body.ID+".json" {
				continue
			}
			email := text(a.Metadata["email"])
			// This computer's own Claude login does not say whose it is; the provider does.
			if email == "" && a.Provider == "claude" && strings.HasPrefix(body.ID, "native-") {
				if email = claudeEmail(ctx, m, a); email == "" {
					return http.StatusBadGateway, nil
				}
			}
			if a.Attributes["api_key"] != "" {
				return http.StatusOK, gin.H{"provider": a.Provider, "email": email, "kind": "api_key", "credential_ref": "config-index:" + a.Provider + ":" + a.EnsureIndex(), "auth_id": a.ID}
			}
			return http.StatusOK, gin.H{"provider": a.Provider, "email": email, "kind": "oauth", "credential_ref": a.FileName, "auth_id": a.ID}
		}
		return http.StatusNotFound, nil
	}))
	engine.POST("/ao/login/device/start", call(func(ctx context.Context, body request) (int, any) { return b.Logins.start(ctx, body.ID) }))
	engine.GET("/ao/login/status", call(func(_ context.Context, body request) (int, any) { return b.Logins.status(body.ID) }))
	engine.DELETE("/ao/login/status", call(func(_ context.Context, body request) (int, any) {
		b.Logins.settle(body.ID, "cancelled")
		return http.StatusNoContent, nil
	}))
	engine.POST("/ao/tag-api-key", call(func(ctx context.Context, body request) (int, any) {
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		a, err := waitAuth(wait, m, func(a *coreauth.Auth) bool {
			return body.APIKey != "" && a.Provider == body.Provider && a.Attributes["api_key"] == body.APIKey && strings.TrimRight(a.Attributes["base_url"], "/") == strings.TrimRight(body.BaseURL, "/")
		})
		if err != nil {
			return http.StatusNotFound, nil
		}
		if a.Metadata == nil {
			a.Metadata = map[string]any{}
		}
		a.Metadata["ao_login_id"], a.Metadata["email"] = body.ID, cmp.Or(strings.TrimSpace(body.Label), body.Provider+" API key")
		if _, err = m.Update(ctx, a); err != nil {
			return http.StatusInternalServerError, nil
		}
		return http.StatusOK, gin.H{"auth_id": a.ID}
	}))
}
