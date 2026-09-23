package session

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestWorkspaceLocationReturnsOnlyLiveSessionWorkspace(t *testing.T) {
	workspace := t.TempDir()
	store := newFakeStore()
	store.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: workspace},
	}

	got, err := (&Service{store: store}).WorkspaceLocation(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != workspace {
		t.Fatalf("WorkspaceLocation() = %q, want %q", got, workspace)
	}
}

func TestWorkspaceLocationDoesNotFallBackToProjectCheckout(t *testing.T) {
	store := newFakeStore()
	store.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", ProjectID: "ao"}
	store.projects["ao"] = domain.ProjectRecord{ID: "ao", Path: t.TempDir()}

	_, err := (&Service{store: store}).WorkspaceLocation(context.Background(), "ao-1")
	assertAPIErrorCode(t, err, "SESSION_WORKSPACE_NOT_FOUND")
}

func TestWorkspaceLocationRejectsMissingDirectory(t *testing.T) {
	store := newFakeStore()
	store.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: t.TempDir() + "/gone"},
	}

	_, err := (&Service{store: store}).WorkspaceLocation(context.Background(), "ao-1")
	assertAPIErrorCode(t, err, "SESSION_WORKSPACE_NOT_FOUND")
}

func TestWorkspaceLocationPreservesStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.getSessionErr = errors.New("storage unavailable")

	_, err := (&Service{store: store}).WorkspaceLocation(context.Background(), "ao-1")
	if err == nil || !errors.Is(err, store.getSessionErr) {
		t.Fatalf("WorkspaceLocation() error = %v, want wrapped storage failure", err)
	}
}

func assertAPIErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != code {
		t.Fatalf("error = %v, want API code %s", err, code)
	}
}
