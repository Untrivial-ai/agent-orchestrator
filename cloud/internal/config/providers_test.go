package config

import (
	"testing"
)

func TestResolveAvailableProvidersDefaultsToSingle(t *testing.T) {
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDERS", "")
	got, err := resolveAvailableProviders("nodeops", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "nodeops" {
		t.Fatalf("got %v, want [nodeops]", got)
	}
}

func TestResolveAvailableProvidersParsesListAndKeepsDefaultFirst(t *testing.T) {
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDERS", "coder, nodeops")
	got, err := resolveAvailableProviders("nodeops", false)
	if err != nil {
		t.Fatal(err)
	}
	// The default is always first, and each provider appears once.
	if len(got) != 2 || got[0] != "nodeops" || got[1] != "coder" {
		t.Fatalf("got %v, want [nodeops coder]", got)
	}
}

func TestResolveAvailableProvidersDeduplicatesAndTrims(t *testing.T) {
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDERS", " CODER , coder , nodeops ")
	got, err := resolveAvailableProviders("nodeops", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "nodeops" || got[1] != "coder" {
		t.Fatalf("got %v, want [nodeops coder]", got)
	}
}

func TestResolveAvailableProvidersRejectsUnknown(t *testing.T) {
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDERS", "coder, bogus")
	if _, err := resolveAvailableProviders("nodeops", false); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestResolveAvailableProvidersRejectsNonHostedProviderWhenHosted(t *testing.T) {
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDERS", "coder, docker")
	if _, err := resolveAvailableProviders("coder", true); err == nil {
		t.Fatal("expected an error: docker is not permitted in hosted environments")
	}
}

func TestResolveAvailableProvidersAllowsCoderAndNodeOpsWhenHosted(t *testing.T) {
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDERS", "coder")
	got, err := resolveAvailableProviders("nodeops", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "nodeops" || got[1] != "coder" {
		t.Fatalf("got %v, want [nodeops coder]", got)
	}
}

func TestProvidersRequireWorkerHome(t *testing.T) {
	if providersRequireWorkerHome([]string{"ecs"}) {
		t.Fatal("ecs does not require a worker home")
	}
	if !providersRequireWorkerHome([]string{"ecs", "coder"}) {
		t.Fatal("coder requires a worker home")
	}
}
