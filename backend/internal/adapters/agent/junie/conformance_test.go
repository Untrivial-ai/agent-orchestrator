package junie

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type releaseContract struct {
	Build                    string   `json:"build"`
	MarketingVersion         string   `json:"marketingVersion"`
	ReleaseFeed              string   `json:"releaseFeed"`
	HooksChannel             string   `json:"hooksChannel"`
	RequiredFlags            []string `json:"requiredFlags"`
	TUIRegistrationEligible  bool     `json:"tuiRegistrationEligible"`
	ChatRegistrationEligible bool     `json:"chatRegistrationEligible"`
}

func TestPinnedContractStopsEAPRegistration(t *testing.T) {
	f, err := os.Open("testdata/contract-v3196.4.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var c releaseContract
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.HooksChannel != "stable" && c.TUIRegistrationEligible {
		t.Fatal("EAP hooks cannot enable TUI registration")
	}
	if c.HooksChannel != "eap" || c.TUIRegistrationEligible || c.ChatRegistrationEligible {
		t.Fatalf("unexpected candidate gate: %+v", c)
	}
	help, err := os.ReadFile("testdata/junie-help-v3196.4.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range c.RequiredFlags {
		if !strings.Contains(string(help), flag) {
			t.Errorf("captured help missing %s", flag)
		}
	}
}
func TestLiveJunieContract(t *testing.T) {
	if os.Getenv("AO_LIVE_JUNIE") != "1" {
		t.Skip("set AO_LIVE_JUNIE=1 with an authenticated disposable Junie environment")
	}
	t.Fatal("build 3196.4 hooks are EAP; live production gate intentionally fails closed")
}
