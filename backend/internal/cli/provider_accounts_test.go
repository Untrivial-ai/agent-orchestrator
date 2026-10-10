package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpawnAccountFlagReachesTheDaemonAsProviderAccountID(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--project", "demo"}, ""},
		{[]string{"--project", "demo", "--account", "  "}, ""},
		{[]string{"--project", "demo", "--account", "  account-a  ", "--mode", "chat"}, "account-a"},
		{[]string{"--standalone", "--account", "account-b"}, "account-b"},
	} {
		cfg := setConfigEnv(t)
		t.Setenv("AO_SESSION_ID", "")
		var received map[string]json.RawMessage
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
				_, _ = io.WriteString(w, `{"project":{"id":"demo","name":"Demo","path":"/repo/demo","config":{"worker":{"agent":"codex"}}}}`)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/readiness/ensure":
				_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
				_ = json.NewDecoder(r.Body).Decode(&received)
				_, _ = io.WriteString(w, `{"session":{"id":"new-session","status":"idle","displayName":"account worker"}}`)
			default:
				http.NotFound(w, r)
			}
		}))
		writeRunFileFor(t, cfg, server)
		args := append([]string{"spawn", "--name", "account worker", "--agent", "codex"}, tc.args...)
		_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
		server.Close()
		if err != nil {
			t.Fatalf("%v: spawn=%v stderr=%s", tc.args, err, errOut)
		}
		raw, sent := received["providerAccountId"]
		if sent != (tc.want != "") || (sent && string(raw) != `"`+tc.want+`"`) || string(received["harness"]) != `"codex"` {
			t.Fatalf("%v: body=%v", tc.args, received)
		}
	}
}
