package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	fsbrowsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/fsbrowser"
)

// DirectoryBrowserService lists directories on the daemon host.
type DirectoryBrowserService interface {
	List(ctx context.Context, path string) (fsbrowsersvc.Listing, error)
}

// FSController owns the read-only /fs routes used by remote clients to browse
// for a project path. Listing is directories-only and dotfile-free by design:
// the connection credential already authorizes spawning agents (shell access),
// so this reveals nothing new — but there is no reason to serve more than the
// picker needs.
type FSController struct {
	Svc DirectoryBrowserService
}

// Register mounts fs REST routes on the supplied router.
func (c *FSController) Register(r chi.Router) {
	r.Get("/fs/dirs", c.listDirs)
}

func (c *FSController) listDirs(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/fs/dirs")
		return
	}
	listing, err := c.Svc.List(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}

	out := ListDirsResponse{Path: listing.Path, Parent: listing.Parent, Entries: make([]FSEntry, len(listing.Entries)), Truncated: listing.Truncated}
	for i, entry := range listing.Entries {
		out.Entries[i] = FSEntry{Name: entry.Name, Path: entry.Path, GitRepo: entry.GitRepo}
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}
