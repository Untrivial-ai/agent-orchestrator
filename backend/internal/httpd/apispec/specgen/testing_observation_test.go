package specgen_test

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec/specgen"
)

func TestTestingObservationAndElementAddressingContract(t *testing.T) {
	data, err := specgen.Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]any
		Components struct{ Schemas map[string]openAPISchemaNode }
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Paths["/api/v1/testing/attempts/{attemptId}/tools/observe"]; !ok {
		t.Fatal("observe route missing")
	}
	for _, name := range []string{"TestClickRequest", "TestTypeRequest"} {
		schema := doc.Components.Schemas[name]
		if len(schema.OneOf) != 2 || !slices.Contains(schema.OneOf[0].Required, "elementId") || !slices.Contains(schema.OneOf[1].Required, "x") || !slices.Contains(schema.OneOf[1].Required, "y") || slices.Contains(schema.Required, "x") || slices.Contains(schema.Required, "y") {
			t.Fatal("address alternatives lost", name, schema)
		}
	}
	for _, field := range []string{"elements", "truncated"} {
		if _, ok := doc.Components.Schemas["TestScreenshot"].Properties[field]; !ok {
			t.Fatal("same-capture metadata missing", field)
		}
	}
}
