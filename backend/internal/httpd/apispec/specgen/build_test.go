package specgen_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec/specgen"
)

type openAPISchemaNode struct {
	Ref        string                       `yaml:"$ref"`
	Type       any                          `yaml:"type"`
	Format     string                       `yaml:"format"`
	Enum       []string                     `yaml:"enum"`
	Required   []string                     `yaml:"required"`
	Properties map[string]openAPISchemaNode `yaml:"properties"`
	AnyOf      []openAPISchemaNode          `yaml:"anyOf"`
	OneOf      []openAPISchemaNode          `yaml:"oneOf"`
}

// TestBuild_MatchesEmbedded is the drift guard: the committed (embedded)
// openapi.yaml must equal fresh Build() output. If this fails, run
// `go generate ./...` and commit the result.
func TestBuild_MatchesEmbedded(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	embedded := apispec.Default().YAML()
	if !bytes.Equal(normalizeYAML(got), normalizeYAML(embedded)) {
		t.Fatalf("embedded openapi.yaml is stale — run `go generate ./...` and commit.\n"+
			"len(fresh)=%d len(embedded)=%d", len(got), len(embedded))
	}
}

func TestBuild_SpawnHarnessEnumIncludesPrimeAgent(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(got), "          - prime-agent\n") {
		t.Fatal("SpawnSessionRequest harness enum does not contain prime-agent")
	}
}

func TestBuild_DelegateAgentEnumIncludesPrimeAgent(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}
	agents := doc.Components.Schemas["DelegateTaskRequest"].Properties["agent"].Enum
	if !slices.Contains(agents, "prime-agent") {
		t.Fatalf("DelegateTaskRequest agent enum = %v, want prime-agent", agents)
	}
}

func TestBuild_UsageEstimatedCostIsNamedReusableAndNullable(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]openAPISchemaNode `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}

	cost, ok := doc.Components.Schemas["EstimatedCostResponse"]
	if !ok {
		t.Fatal("EstimatedCostResponse is not a named schema")
	}
	for _, field := range []string{"totalNanos", "inputNanos", "cachedInputNanos", "outputNanos", "coverage"} {
		if !slices.Contains(cost.Required, field) {
			t.Fatalf("EstimatedCostResponse required = %v, missing %s", cost.Required, field)
		}
	}
	for _, field := range []string{"totalNanos", "inputNanos", "cachedInputNanos", "outputNanos"} {
		if cost.Properties[field].Format != "int64" {
			t.Fatalf("EstimatedCostResponse.%s format = %q, want int64", field, cost.Properties[field].Format)
		}
	}
	if want := []string{"complete", "partial"}; !slices.Equal(cost.Properties["coverage"].Enum, want) {
		t.Fatalf("coverage enum = %v, want %v", cost.Properties["coverage"].Enum, want)
	}

	const costRef = "#/components/schemas/EstimatedCostResponse"
	for schemaName, propertyName := range map[string]string{
		"CompactSessionUsageResponse": "estimatedCost",
		"UsageTotalsResponse":         "estimatedCost",
	} {
		property := doc.Components.Schemas[schemaName].Properties[propertyName]
		if property.Type != nil {
			t.Fatalf("%s.%s has contradictory outer type %v", schemaName, propertyName, property.Type)
		}
		if !schemaContainsRef(property, costRef) || !schemaAllowsNull(property) {
			t.Fatalf("%s.%s = %+v, want nullable %s", schemaName, propertyName, property, costRef)
		}
	}
	// One model is one row. The billing provider is a pricing input the
	// aggregate has already applied, so it must not reappear here and split a
	// model apart by AO's own attribution state.
	model := doc.Components.Schemas["UsageModelResponse"]
	if slices.Contains(model.Required, "providerId") {
		t.Fatalf("UsageModelResponse still exposes providerId: %v", model.Required)
	}
}

func schemaContainsRef(node openAPISchemaNode, want string) bool {
	if node.Ref == want {
		return true
	}
	for _, child := range append(node.AnyOf, node.OneOf...) {
		if child.Ref == want {
			return true
		}
	}
	return false
}

func schemaAllowsNull(node openAPISchemaNode) bool {
	containsNull := func(value any) bool {
		switch typed := value.(type) {
		case string:
			return typed == "null"
		case []any:
			return slices.Contains(typed, any("null"))
		default:
			return false
		}
	}
	if node.Type != nil && !containsNull(node.Type) {
		return false
	}
	if len(node.AnyOf) > 0 {
		for _, child := range node.AnyOf {
			if schemaAllowsNull(child) {
				return true
			}
		}
		return false
	}
	if len(node.OneOf) > 0 {
		matches := 0
		for _, child := range node.OneOf {
			if schemaAllowsNull(child) {
				matches++
			}
		}
		return matches == 1
	}
	if node.Ref != "" {
		return false
	}
	return containsNull(node.Type)
}

// TestBuild_Deterministic guards against nondeterministic output (which would
// make the drift check flaky in CI).
func TestBuild_Deterministic(t *testing.T) {
	a, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	b, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("Build() is not deterministic across calls")
	}
}

func normalizeYAML(in []byte) []byte {
	return bytes.ReplaceAll(in, []byte("\r\n"), []byte("\n"))
}
