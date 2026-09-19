package httpd

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// recoverPanics turns a handler panic into a 500 instead of crashing the
// daemon, logging the panic and its Go stack.
func recoverPanics(log *slog.Logger) func(http.Handler) http.Handler {
	log = loggerOrDefault(log)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := string(debug.Stack())
					log.Error("http handler panic",
						"id", middleware.GetReqID(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
						"panic", fmt.Sprint(rec),
						"stack", stack,
					)
					writeRecoveredError(w, r)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func writeRecoveredError(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal_error", "INTERNAL_ERROR", "Internal server error", nil)
		return
	}
	envelope.WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"status": "error",
	})
}
