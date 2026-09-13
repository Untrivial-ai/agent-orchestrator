package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Both commands must report what the claim response establishes, without
// interpreting branchChanged=false as proof that HEAD matches the PR branch.
func TestClaimCommandsCheckoutOutput(t *testing.T) {
	for _, tc := range []struct {
		name          string
		branchChanged bool
		emptyPRs      bool
		wantCheckout  string
	}{
		{"metadata only", false, false, "not performed; workspace unchanged"},
		{"checkout reported by daemon", true, false, "switched to PR branch"},
		{"metadata only without PR facts", false, true, "not performed; workspace unchanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			prs := `[{"url":"https://github.com/acme/repo/pull/7","number":7}]`
			if tc.emptyPRs {
				prs = `[]`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/projects/demo":
					_, _ = io.WriteString(w, `{"project":{"id":"demo","repo":"https://github.com/acme/repo"}}`)
				case "GET /api/v1/sessions/demo-1", "POST /api/v1/sessions":
					_, _ = io.WriteString(w, `{"session":{"id":"demo-1","projectId":"demo","status":"idle"}}`)
				case "POST /api/v1/sessions/demo-1/pr/claim":
					_, _ = fmt.Fprintf(w, `{"ok":true,"sessionId":"demo-1","prs":%s,"branchChanged":%t}`, prs, tc.branchChanged)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			writeRunFileFor(t, cfg, srv)
			deps := Deps{ProcessAlive: func(int) bool { return true }}
			for _, args := range [][]string{
				{"session", "claim-pr", "demo-1", "7"},
				{"spawn", "--project", "demo", "--agent", "codex", "--name", "worker", "--skip-agent-check", "--claim-pr", "7"},
			} {
				out, errOut, err := executeCLI(t, deps, args...)
				if err != nil {
					t.Fatalf("%v: %v stderr=%s", args, err, errOut)
				}
				if want := "  checkout: " + tc.wantCheckout + "\n"; !strings.Contains(out, want) {
					t.Errorf("%v output = %q, want %q", args, out, want)
				}
				if strings.Contains(out, "already on PR branch") || strings.Contains(out, "switched to PR branch") != tc.branchChanged {
					t.Errorf("%v output contradicts branchChanged=%t: %s", args, tc.branchChanged, out)
				}
			}
			// spawn has no --json mode; its human output above must agree with
			// the same daemon field that session claim-pr exposes as JSON.
			out, errOut, err := executeCLI(t, deps, "session", "claim-pr", "demo-1", "7", "--json")
			if err != nil {
				t.Fatalf("claim-pr --json: %v stderr=%s", err, errOut)
			}
			var got struct {
				BranchChanged *bool `json:"branchChanged"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil || got.BranchChanged == nil || *got.BranchChanged != tc.branchChanged {
				t.Fatalf("JSON branchChanged does not match daemon response: err=%v out=%s", err, out)
			}
		})
	}
}

func TestClaimCommandsHelpDocumentsMetadataOnly(t *testing.T) {
	for _, args := range [][]string{{"session", "claim-pr", "--help"}, {"spawn", "--help"}} {
		out, _, err := executeCLI(t, Deps{}, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "metadata only") || !strings.Contains(out, "does not check out the PR branch") {
			t.Errorf("%v help must explain metadata-only claiming: %s", args, out)
		}
	}
}
