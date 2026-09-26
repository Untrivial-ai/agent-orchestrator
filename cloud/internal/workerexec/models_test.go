package workerexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiscoverCodexModelsUsesNativeAppServerCatalog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	workspace := t.TempDir()
	binary := filepath.Join(workspace, "codex")
	script := `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{}}'
read initialized
read list
printf '%s\n' '{"id":2,"result":{"data":[{"id":"account-model","displayName":"Account Model","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"high"}]}]}}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	models, err := DiscoverCodexModels(context.Background(), binary, workspace)
	if err != nil {
		t.Fatalf("discover models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "account-model" || len(models[0].Efforts) != 1 || models[0].Efforts[0] != "high" {
		t.Fatalf("models = %+v", models)
	}
}

func TestParseCodexModelListFiltersHiddenModelsAndRetainsEfforts(t *testing.T) {
	models, cursor, err := parseCodexModelList(json.RawMessage(`{
		"data": [
			{"id":"gpt-test","displayName":"GPT Test","isDefault":true,"defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]},
			{"id":"hidden","hidden":true}
		], "nextCursor":"page-2"
	}`))
	if err != nil {
		t.Fatalf("parse models: %v", err)
	}
	if cursor != "page-2" || len(models) != 1 || models[0].ID != "gpt-test" ||
		models[0].DisplayName != "GPT Test" || !models[0].Default ||
		models[0].DefaultEffort != "medium" || len(models[0].Efforts) != 2 ||
		models[0].Efforts[1] != "high" {
		t.Fatalf("models = %+v, cursor = %q", models, cursor)
	}
}
