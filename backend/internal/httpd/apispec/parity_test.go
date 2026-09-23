package apispec_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	yaml "gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// stubDeviceRoster and stubDeviceLive give TestRouteSpecParity a non-nil
// DeviceRoster so mountMobileDevices (which mounts its routes unconditionally
// and answers 503 DEVICE_REGISTRY_UNAVAILABLE when the registry is unavailable)
// registers its routes here — otherwise the roster's spec operations below
// would have no mounted route to match.
type stubDeviceRoster struct{}

func (stubDeviceRoster) List() []mobilebridge.PushDevice { return nil }
func (stubDeviceRoster) SetMuted(string, bool) error     { return nil }
func (stubDeviceRoster) Delete(string) error             { return nil }

type stubDeviceLive struct{}

func (stubDeviceLive) Live() map[string]bool { return nil }

func (stubDeviceLive) LastSeen(string) (time.Time, bool) { return time.Time{}, false }

// TestRouteSpecParity asserts the mounted /api/v1 routes and the OpenAPI
// operations are in 1:1 correspondence — so a route can't be added without
// spec coverage, and the spec can't describe a route that isn't served.
func TestRouteSpecParity(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Mobile carries a non-nil MobileController so mountMobile (which, like
	// mountControl, skips mounting entirely on a nil controller) registers its
	// routes here — otherwise the mobile spec operations below would have no
	// mounted route to match.
	deps := httpd.APIDeps{
		Mobile:       &controllers.MobileController{},
		DeviceRoster: stubDeviceRoster{},
		DeviceLive:   stubDeviceLive{},
	}
	router := httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{})

	mounted := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/v1/") && route != "/api/v1/openapi.yaml" {
			mounted[strings.ToUpper(method)+" "+route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if len(mounted) == 0 {
		t.Fatal("no /api/v1 routes mounted — router wiring changed?")
	}

	// Forward: every mounted route resolves to an operation slice.
	for r := range mounted {
		mp := strings.SplitN(r, " ", 2)
		if apispec.Default().Operation(mp[0], mp[1]) == nil {
			t.Errorf("mounted route %s has no OpenAPI operation", r)
		}
	}

	// Reverse: every spec operation is a mounted route.
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(apispec.Default().YAML(), &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	httpMethods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	for path, item := range doc.Paths {
		for method := range item {
			if !httpMethods[method] {
				continue // skip parameters, summary, etc.
			}
			key := strings.ToUpper(method) + " " + path
			if !mounted[key] {
				t.Errorf("spec operation %s has no mounted route", key)
			}
		}
	}
}
