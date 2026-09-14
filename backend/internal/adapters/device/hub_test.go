package device

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestPrepareStreamStartsSelectedIOSHelperAndWaitsForHealth(t *testing.T) {
	const deviceID = "E4988E8F-880D-4E60-B396-5205CFEB193B"
	var starts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/devices":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/vendor/serve-sim/grid/api/start":
			var request struct {
				UDID string `json:"udid"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.UDID != deviceID {
				t.Fatalf("start request = %#v, err = %v", request, err)
			}
			starts.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/vendor/serve-sim/helper/"+deviceID+"/health":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime := &Runtime{hubOrigin: server.URL}
	if err := runtime.PrepareStream(context.Background(), domain.DevicePlatformIOS, deviceID); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("stream helper starts = %d, want 1", starts.Load())
	}
}

func TestPrepareStreamDoesNotStartAppleHelperForAndroid(t *testing.T) {
	runtime := &Runtime{}
	if err := runtime.PrepareStream(context.Background(), domain.DevicePlatformAndroid, "emulator-5554"); err != nil {
		t.Fatal(err)
	}
}
