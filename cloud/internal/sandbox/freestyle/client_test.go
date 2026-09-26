package freestyle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

func testVM(sessionID, orgID, state string) vmView {
	return vmView{
		ID: "vm-123", Slug: slug(sessionID), State: state,
		Metadata: map[string]string{"ao-session-id": sessionID, "ao-org-id": orgID},
	}
}

func TestLifecycleRecoversCreateAndPreservesPausedVM(t *testing.T) {
	t.Parallel()
	const sessionID = "a6205377-5ebd-4d64-9e69-13366ae3bff0"
	const orgID = "5af15ae1-8475-46c8-a966-7f7915ddab9b"
	var mu sync.Mutex
	var vm *vmView
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing API authentication")
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v5/vms/"):
			if vm == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(vm)
		case r.Method == http.MethodPost && r.URL.Path == "/v5/vms":
			creates++
			var body struct {
				Slug               string `json:"slug"`
				SnapshotID         string `json:"snapshotId"`
				IdleTimeoutSeconds int    `json:"idleTimeoutSeconds"`
				Firewall           struct {
					Rules []json.RawMessage `json:"rules"`
				} `json:"firewall"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Slug != slug(sessionID) || body.SnapshotID != "freestyle/ubuntu" || body.IdleTimeoutSeconds != -1 || len(body.Firewall.Rules) != 1 {
				t.Errorf("incorrect VM create request: %+v", body)
			}
			created := testVM(sessionID, orgID, "running")
			vm = &created
			_ = json.NewEncoder(w).Encode(vm)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pause"):
			vm.State = "paused"
			_ = json.NewEncoder(w).Encode(vm)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			vm.State = "running"
			_ = json.NewEncoder(w).Encode(vm)
		case r.Method == http.MethodDelete:
			vm = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	spec := sandbox.Spec{SessionID: sessionID, OrgID: orgID, Name: "AO worker"}
	first, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || creates != 1 {
		t.Fatalf("create was not idempotent: %v %v, count=%d", first.ID, second.ID, creates)
	}
	if err := c.Stop(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := c.Get(context.Background(), first.ID)
	if err != nil || paused.State != sandbox.StatePaused {
		t.Fatalf("paused VM = %+v, %v", paused, err)
	}
	if err := c.Resume(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), first.ID); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("deleted VM get = %v", err)
	}
}

func TestCreateRefusesForeignSlug(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vm := testVM("session-1", "different-org", "running")
		_ = json.NewEncoder(w).Encode(vm)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Create(context.Background(), sandbox.Spec{SessionID: "session-1", OrgID: "org-1"})
	if err == nil || !strings.Contains(err.Error(), "another organization") {
		t.Fatalf("foreign VM adopted: %v", err)
	}
}

func TestCreateRetriesWhenFreestyleHasNoCapacity(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusConflict, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			code := "LIMIT_EXCEEDED"
			if status == http.StatusConflict {
				code = "CONFLICT"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": "no VM capacity"})
			}))
			defer server.Close()
			c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Create(context.Background(), sandbox.Spec{SessionID: "session-1", OrgID: "org-1"})
			if !errors.Is(err, sandbox.ErrAtCapacity) {
				t.Fatalf("create status %d should retry, got %v", status, err)
			}
		})
	}
}

func TestCreateConflictDoesNotRetryForeignSlug(t *testing.T) {
	t.Parallel()
	lookups := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			lookups++
			if lookups == 1 {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(testVM("session-1", "different-org", "running"))
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"CONFLICT","message":"slug already used"}`)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Create(context.Background(), sandbox.Spec{SessionID: "session-1", OrgID: "org-1"})
	if err == nil || errors.Is(err, sandbox.ErrAtCapacity) || !strings.Contains(err.Error(), "another organization") {
		t.Fatalf("foreign slug conflict = %v", err)
	}
}

func TestPauseWaitsForStartingVM(t *testing.T) {
	t.Parallel()
	reads := 0
	paused := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads++
			state := "starting"
			if reads > 1 {
				state = "running"
			}
			_ = json.NewEncoder(w).Encode(testVM("session-1", "org-1", state))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pause") {
			paused = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(context.Background(), "vm-123"); err != nil {
		t.Fatal(err)
	}
	if !paused || reads < 2 {
		t.Fatalf("pause did not wait: paused=%v reads=%d", paused, reads)
	}
}

func TestBootstrapUploadsBinariesAndLaunchesUnprivilegedWorker(t *testing.T) {
	t.Parallel()
	var commands []string
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec-await"):
			var body struct {
				Command   string `json:"command"`
				LinuxUser string `json:"linuxUser"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.LinuxUser != "root" {
				t.Errorf("bootstrap ran as %q", body.LinuxUser)
			}
			commands = append(commands, body.Command)
			_, _ = io.WriteString(w, `{"statusCode":0}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/fs/write"):
			if r.URL.Query().Get("mode") != "493" {
				t.Errorf("write mode = %q", r.URL.Query().Get("mode"))
			}
			bytes, _ := io.ReadAll(r.Body)
			writes = append(writes, r.URL.Query().Get("path")+":"+string(bytes))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.BootstrapWorker(context.Background(), "vm-123", sandbox.WorkerBootstrap{
		Binary: []byte("worker"), Destination: "/usr/local/bin/ao-worker",
		HelperBinary: []byte("helper"), HelperDestination: "/usr/local/bin/ao",
		User: "ao-worker", Environment: map[string]string{
			"AO_WORKER_BOOTSTRAP_TOKEN": "one-time-ticket", "AO_CLOUD_PUBLIC_URL": "https://api.example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 || writes[0] != "/usr/local/bin/ao-worker:worker" || writes[1] != "/usr/local/bin/ao:helper" {
		t.Fatalf("uploads = %v", writes)
	}
	if len(commands) != 2 || !strings.Contains(commands[1], "runuser --user 'ao-worker'") ||
		!strings.Contains(commands[1], "AO_WORKER_BOOTSTRAP_TOKEN='one-time-ticket'") ||
		!strings.Contains(commands[1], "pkill -f") {
		t.Fatalf("launch command was incomplete: %v", commands)
	}
}

func TestBackgroundOperationPollsUntilResult(t *testing.T) {
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/background-requests/bgr-123" {
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			_, _ = io.WriteString(w, `{"id":"vm-123","slug":"ao-session-1","state":"running","metadata":{"ao-session-id":"session-1","ao-org-id":"org-1"}}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"requestId":"bgr-123","resultUrl":"/v5/background-requests/bgr-123"}`)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(context.Background(), "vm-123")
	if err != nil || polls != 2 {
		t.Fatalf("background result: polls=%d error=%v", polls, err)
	}
}

func TestLargeWorkerUsesChunkedUpload(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("worker-binary"), 1400000)
	var received []byte
	committed := false
	chunks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/fs/uploads"):
			var body struct {
				Path string `json:"path"`
				Size int    `json:"size"`
				Mode int    `json:"mode"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Path != "/usr/local/bin/ao-worker" || body.Size != len(data) || body.Mode != 493 {
				t.Errorf("upload setup = %+v", body)
			}
			_, _ = io.WriteString(w, `{"uploadId":"upload-1","receivedBytes":0}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/fs/uploads/upload-1"):
			if r.URL.Query().Get("offset") != strconv.Itoa(len(received)) {
				t.Errorf("offset = %q", r.URL.Query().Get("offset"))
			}
			part, _ := io.ReadAll(r.Body)
			received = append(received, part...)
			chunks++
			_ = json.NewEncoder(w).Encode(map[string]int{"receivedBytes": len(received)})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/fs/uploads/upload-1/commit"):
			committed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeFile(context.Background(), "vm-123", "/usr/local/bin/ao-worker", data); err != nil {
		t.Fatal(err)
	}
	if !committed || chunks < 2 || !bytes.Equal(received, data) {
		t.Fatalf("chunked upload incomplete: committed=%v chunks=%d received=%d", committed, chunks, len(received))
	}
}

func TestBootstrapRejectsUnsafeDestination(t *testing.T) {
	c, err := New(Config{BaseURL: "https://api.freestyle.sh", APIKey: "test-key", SnapshotID: "freestyle/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.BootstrapWorker(context.Background(), "vm-123", sandbox.WorkerBootstrap{
		Binary: []byte("worker"), Destination: "/tmp/../worker", User: "ao-worker",
	})
	if err == nil {
		t.Fatal("unsafe destination accepted")
	}
}
