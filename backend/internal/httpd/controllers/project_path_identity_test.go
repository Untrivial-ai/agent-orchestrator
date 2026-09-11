package controllers_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectsAPI_EquivalentPathsReturnRegisteredIdentity(t *testing.T) {
	srv := newTestServer(t)
	repo := gitRepo(t, "physical-repo")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(alias)+`,"projectId":"original"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repo)+`,"projectId":"duplicate"}`)
	assertErrorCode(t, body, status, http.StatusConflict, "PATH_ALREADY_REGISTERED")
	var conflict struct {
		Details struct {
			ExistingProjectID string `json:"existingProjectId"`
		} `json:"details"`
	}
	mustJSON(t, body, &conflict)
	if conflict.Details.ExistingProjectID != "original" {
		t.Fatalf("conflict identity: %s", body)
	}
	body, status, _ = doRequest(t, srv, "GET", "/api/v1/projects", "")
	var result struct {
		Projects []projectBody `json:"projects"`
	}
	mustJSON(t, body, &result)
	if status != http.StatusOK || len(result.Projects) != 1 {
		t.Fatalf("duplicate registered: %d %s", status, body)
	}
}
