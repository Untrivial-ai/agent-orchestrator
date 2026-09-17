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

// This fixture records outstanding live evidence. Experimental terminal
// registration does not claim conformance or enable ACP Chat.
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
	TUIConformanceVerified  bool     `json:"tuiConformanceVerified"`
	ChatConformanceVerified bool     `json:"chatConformanceVerified"`
	MissingEvidence         []string `json:"missingEvidence"`
}

func TestJunieExperimentalTerminalKeepsChatUnregistered(t *testing.T) {
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
	if contract.TUIConformanceVerified || contract.ChatConformanceVerified {
		t.Fatal("fixture must retain the unverified conformance status")
	}
	if !domain.AgentHarness("junie").IsKnown() {
		t.Fatal("experimental Junie must be selectable")
	}
	reg, err := agentregistry.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("junie"); !ok {
		t.Fatal("experimental Junie terminal adapter is missing")
	}
	if chatregistry.Build(nil).SupportsChat(domain.AgentHarness("junie")) {
		t.Fatal("Junie Chat was registered without authenticated ACP conformance")
	}
}
