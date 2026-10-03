package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrgCoderConfigRoundTripNormalizes(t *testing.T) {
	t.Parallel()
	encoded, err := EncodeOrgCoderConfig(OrgCoderConfig{
		BaseURL:             "  https://coder.example.com/  ",
		Owner:               " ao-bot ",
		TemplateID:          " 2a2e262c-b31c-4202-946d-a19ad45d1fd2 ",
		AgentName:           " dev ",
		Parameters:          map[string]string{"region": "eu"},
		EndpointServiceName: "  com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0  ",
		Region:              " eu-north-1 ",
		// DurableRoot omitted on purpose: the default must be filled.
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeOrgCoderConfig(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://coder.example.com" {
		t.Fatalf("baseUrl = %q, want trimmed without trailing slash", cfg.BaseURL)
	}
	if cfg.Owner != "ao-bot" || cfg.TemplateID != "2a2e262c-b31c-4202-946d-a19ad45d1fd2" || cfg.AgentName != "dev" {
		t.Fatalf("fields not trimmed: %+v", cfg)
	}
	if cfg.EndpointServiceName != "com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0" || cfg.Region != "eu-north-1" {
		t.Fatalf("PrivateLink fields not trimmed/round-tripped: %+v", cfg)
	}
	if cfg.DurableRoot != DefaultOrgCoderDurableRoot {
		t.Fatalf("durableRoot = %q, want default %q", cfg.DurableRoot, DefaultOrgCoderDurableRoot)
	}
	if cfg.Parameters["region"] != "eu" {
		t.Fatalf("parameters lost: %+v", cfg.Parameters)
	}
}

func TestOrgCoderConfigNeverCarriesAToken(t *testing.T) {
	t.Parallel()
	// The struct has no token field; a token in the raw JSON must not survive a
	// round trip into the stored config.
	encoded, err := EncodeOrgCoderConfig(OrgCoderConfig{
		BaseURL: "https://coder.example.com", Owner: "ao-bot",
		TemplateID: "2a2e262c-b31c-4202-946d-a19ad45d1fd2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "token") {
		t.Fatalf("encoded config carries a token field: %s", encoded)
	}
}

// The PrivateLink fields are optional: omitted they must not appear in the
// encoded config, and a decode of a config without them yields blank values.
func TestOrgCoderConfigPrivateLinkFieldsOptional(t *testing.T) {
	t.Parallel()
	encoded, err := EncodeOrgCoderConfig(OrgCoderConfig{
		BaseURL: "https://coder.example.com", Owner: "ao-bot",
		TemplateID: "2a2e262c-b31c-4202-946d-a19ad45d1fd2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "endpointServiceName") || strings.Contains(string(encoded), "region") {
		t.Fatalf("blank PrivateLink fields leaked into the encoded config: %s", encoded)
	}
	cfg, err := DecodeOrgCoderConfig(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EndpointServiceName != "" || cfg.Region != "" {
		t.Fatalf("expected blank PrivateLink fields, got %+v", cfg)
	}
}

func TestDecodeOrgCoderConfigRejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := DecodeOrgCoderConfig(nil); err == nil {
		t.Fatal("decoded an empty config")
	}
	if _, err := DecodeOrgCoderConfig(json.RawMessage(`not json`)); err == nil {
		t.Fatal("decoded invalid JSON")
	}
}
