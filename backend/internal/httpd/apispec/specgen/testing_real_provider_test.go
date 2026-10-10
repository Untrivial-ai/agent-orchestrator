package specgen_test

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec/specgen"
)

func TestTestingRealProviderWireContract(t *testing.T) {
	data, err := specgen.Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct{ Schemas map[string]openAPISchemaNode }
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	start := doc.Components.Schemas["StartTestingAttemptRequest"]
	for _, field := range []string{"model", "effort"} {
		if _, ok := start.Properties[field]; !ok {
			t.Fatalf("missing investigator %s", field)
		}
	}
	query := doc.Components.Schemas["TestDaemonQueryRequest"]
	if _, ok := query.Properties["sessionId"]; !ok {
		t.Fatal("missing closed target session selector")
	}
	for _, resource := range []string{"projects", "sessions", "reviews", "conversation"} {
		if !slices.Contains(query.Properties["resource"].Enum, resource) {
			t.Fatalf("missing allowed resource %s", resource)
		}
	}
}
