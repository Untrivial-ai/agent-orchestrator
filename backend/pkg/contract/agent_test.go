package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

var agentCapabilities = []contract.AgentCapability{
	contract.CapabilityInterfaceChat,
	contract.CapabilityInterfaceTUI,
	contract.CapabilityModelCatalog,
	contract.CapabilityCustomModel,
	contract.CapabilityAttachments,
	contract.CapabilityBrowserPreview,
	contract.CapabilityReviewExecute,
	contract.CapabilityResume,
}

func TestHasAgentCapability(t *testing.T) {
	profile := contract.AgentProfile{
		Capabilities: []contract.AgentCapability{
			contract.CapabilityInterfaceChat,
			contract.CapabilityResume,
		},
	}

	if !contract.HasAgentCapability(profile, contract.CapabilityResume) {
		t.Fatal("resume capability was not found")
	}
	if contract.HasAgentCapability(profile, contract.CapabilityAttachments) {
		t.Fatal("undeclared attachment capability was found")
	}
}

func TestAgentProfileJSONKeepsPolicyInAvailability(t *testing.T) {
	profile := contract.AgentProfile{
		ID:           "runtime-agent",
		Label:        "Runtime Agent",
		Capabilities: []contract.AgentCapability{contract.CapabilityInterfaceChat},
		Availability: contract.AgentAvailability{
			Available:          false,
			Installation:       contract.AgentInstallationInstalled,
			Authentication:     contract.AgentAuthenticationUnauthorized,
			OrganizationPolicy: contract.OrganizationPolicyDenied,
			Reason:             "Disabled for this organization.",
		},
	}

	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}

	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if _, ok := value["organizationPolicy"]; ok {
		t.Fatal("organization policy must not be an intrinsic profile field")
	}
	availability, ok := value["availability"].(map[string]any)
	if !ok ||
		availability["installation"] != "installed" ||
		availability["authentication"] != "unauthorized" ||
		availability["organizationPolicy"] != "denied" {
		t.Fatalf("availability = %#v", value["availability"])
	}
}

func TestSharedAgentVocabulariesMatchGo(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	repoRoot := filepath.Join(filepath.Dir(currentFile), "..", "..", "..")
	specPath := filepath.Join(repoRoot, "contracts", "cloud", "openapi.yaml")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}

	var document struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	capabilities := make([]string, len(agentCapabilities))
	for i, capability := range agentCapabilities {
		capabilities[i] = string(capability)
	}

	productPath := filepath.Join(repoRoot, "packages", "product-ui", "src", "agent-capabilities.ts")
	data, err = os.ReadFile(productPath)
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		schema       string
		productConst string
		want         []string
	}{
		{"AgentCapability", "AGENT_CAPABILITIES", capabilities},
		{
			"AgentInstallationState",
			"AGENT_INSTALLATION_STATES",
			[]string{
				string(contract.AgentInstallationInstalled),
				string(contract.AgentInstallationNotInstalled),
				string(contract.AgentInstallationUnknown),
				string(contract.AgentInstallationNotApplicable),
			},
		},
		{
			"AgentAuthenticationState",
			"AGENT_AUTHENTICATION_STATES",
			[]string{
				string(contract.AgentAuthenticationAuthorized),
				string(contract.AgentAuthenticationUnauthorized),
				string(contract.AgentAuthenticationUnknown),
				string(contract.AgentAuthenticationNotApplicable),
			},
		},
	}

	for _, check := range checks {
		t.Run(check.schema, func(t *testing.T) {
			schema, ok := document.Components.Schemas[check.schema]
			if !ok {
				t.Fatalf("Cloud schema has no %s", check.schema)
			}
			if !slices.Equal(schema.Enum, check.want) {
				t.Fatalf("Cloud %s = %q, want %q", check.schema, schema.Enum, check.want)
			}

			pattern := `(?s)` + regexp.QuoteMeta(check.productConst) + ` = \[(.*?)\] as const`
			block := regexp.MustCompile(pattern).FindSubmatch(data)
			if len(block) != 2 {
				t.Fatalf("product UI has no %s array", check.productConst)
			}
			matches := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1)
			got := make([]string, len(matches))
			for i, match := range matches {
				got[i] = string(match[1])
			}
			if !slices.Equal(got, check.want) {
				t.Fatalf("product UI %s = %q, want %q", check.productConst, got, check.want)
			}
		})
	}
}
