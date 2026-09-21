package httpapi

import (
	"encoding/json"
	"testing"
)

func TestPullRequestFailingChecksFromStoredSnapshot(t *testing.T) {
	checks := json.RawMessage(`[{"name":"unit","status":"completed","conclusion":"failure","html_url":"https://example.test/unit"},{"name":"lint","status":"completed","conclusion":"success"}]`)
	got := pullRequestFailingChecks(checks)
	if len(got) != 1 || got[0].Name != "unit" || got[0].Status != "failed" || got[0].URL != "https://example.test/unit" {
		t.Fatalf("failing checks = %#v", got)
	}
}
