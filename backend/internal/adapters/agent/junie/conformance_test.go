package junie_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	chatregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// This fixture records evidence, not an opt-in switch. Removing the admission
// checks below requires a reviewed stable contract and authenticated live proof.
type candidateContract struct {
	HooksDocumentation string `json:"hooksDocumentation"`
	ReviewedOn         string `json:"reviewedOn"`
	HooksChannel       string `json:"hooksChannel"`
	Releases           []struct {
		Channel          string `json:"channel"`
		Build            string `json:"build"`
		MarketingVersion string `json:"marketingVersion"`
		ReleaseFeed      string `json:"releaseFeed"`
		Artifacts        []struct {
			Platform string `json:"platform"`
			URL      string `json:"url"`
			SHA256   string `json:"sha256"`
		} `json:"artifacts"`
	} `json:"releases"`
	TUIRegistrationEligible  bool     `json:"tuiRegistrationEligible"`
	ChatRegistrationEligible bool     `json:"chatRegistrationEligible"`
	MissingEvidence          []string `json:"missingEvidence"`
}

func TestJunieCandidateRemainsUnregistered(t *testing.T) {
	data, err := os.ReadFile("testdata/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract candidateContract
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		t.Fatal(err)
	}
	if contract.HooksChannel != "eap" || len(contract.MissingEvidence) == 0 {
		t.Fatal("candidate contract must record its EAP release and outstanding live evidence")
	}
	channels := map[string]bool{}
	for _, release := range contract.Releases {
		channels[release.Channel] = true
		if release.Build == "" || len(release.Artifacts) == 0 {
			t.Fatal("each release needs a build and official artifact identities")
		}
		for _, artifact := range release.Artifacts {
			digest, err := hex.DecodeString(artifact.SHA256)
			if err != nil || len(digest) != 32 || artifact.Platform == "" || artifact.URL == "" {
				t.Fatal("each artifact needs its platform, URL and official SHA-256")
			}
		}
	}
	if len(contract.Releases) != 2 || !channels["stable"] || !channels["eap"] {
		t.Fatal("stable and EAP evidence must be separate")
	}
	if contract.TUIRegistrationEligible || contract.ChatRegistrationEligible {
		t.Fatal("unverified candidate cannot enable TUI or Chat")
	}
	if domain.AgentHarness("junie").IsKnown() {
		t.Fatal("Junie must not be a selectable harness before conformance")
	}
	for _, adapter := range agentregistry.Constructors() {
		if adapter.Manifest().ID == "junie" {
			t.Fatal("Junie TUI was registered without stable hook conformance")
		}
	}
	if chatregistry.Build(nil).SupportsChat(domain.AgentHarness("junie")) {
		t.Fatal("Junie Chat was registered without authenticated ACP conformance")
	}
}
