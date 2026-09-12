package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

const maxBrowserProxyBody = 1 << 20

func (s *Server) proxyBrowser(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	target, ok := browserTarget(w, r, chi.URLParam(r, "origin"))
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBrowserProxyBody))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "The browser request body must not exceed 1 MiB.")
		return
	}
	payload, err := json.Marshal(worker.BrowserFetchRequest{
		URL:    target.String(),
		Method: r.Method,
		Headers: map[string]string{
			"Accept":          r.Header.Get("Accept"),
			"Accept-Language": r.Header.Get("Accept-Language"),
			"Content-Type":    r.Header.Get("Content-Type"),
		},
		Body: body,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BROWSER_REQUEST", "The browser request is invalid.")
		return
	}
	result, ok := s.runWorkspaceRequest(w, r, orgID, sessionID, "browser.fetch", payload)
	if !ok {
		return
	}
	var response worker.BrowserFetchResponse
	if json.Unmarshal(result, &response) != nil || response.Status < 100 || response.Status > 599 {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The VM returned an invalid browser response.")
		return
	}
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	if response.CacheControl != "" {
		w.Header().Set("Cache-Control", response.CacheControl)
	}
	// The browser iframe intentionally has an opaque origin so an arbitrary VM
	// page cannot read the Cloud UI's authenticated same-origin APIs. Its own
	// fetch/XHR requests still need to reach this session-scoped proxy.
	w.Header().Set("Access-Control-Allow-Origin", "null")
	pageURL, err := url.Parse(response.URL)
	if err == nil {
		switch contentType := strings.ToLower(response.ContentType); {
		case strings.HasPrefix(contentType, "text/html"):
			response.Body = rewriteBrowserHTML(response.Body, pageURL, orgID, sessionID)
		case strings.HasPrefix(contentType, "text/css"):
			response.Body = rewriteBrowserCSS(response.Body, pageURL, orgID, sessionID)
		case strings.Contains(contentType, "javascript") || strings.Contains(contentType, "ecmascript"):
			response.Body = rewriteBrowserJavaScript(response.Body, pageURL, orgID, sessionID)
		}
	}
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}

func browserTarget(w http.ResponseWriter, r *http.Request, encodedOrigin string) (*url.URL, bool) {
	rawOrigin, err := base64.RawURLEncoding.DecodeString(encodedOrigin)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BROWSER_URL", "The browser URL is invalid.")
		return nil, false
	}
	origin, err := url.Parse(string(rawOrigin))
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_BROWSER_URL", "The browser URL must use http or https.")
		return nil, false
	}
	path := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	origin.Path = ""
	origin.RawPath = ""
	origin.RawQuery = ""
	origin.Path = "/" + path
	origin.RawQuery = r.URL.RawQuery
	origin.Fragment = ""
	return origin, true
}
