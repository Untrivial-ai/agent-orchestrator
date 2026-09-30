package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

func TestProjectCreatePayloadComparisonPreservesRequestIdentity(t *testing.T) {
	for _, test := range []struct {
		name, stored, retry string
		equal               bool
	}{
		{"legacy roles", `{"Config":{"workerAgent":"codex","orchestratorAgent":"claude-code"}}`, `{"Config":{"worker":{"agent":"codex"},"orchestrator":{"agent":"claude-code"}}}`, true},
		{"omitted config", `{"Config":null}`, `{"Config":{}}`, true},
		{"nested agent wins", `{"Config":{"workerAgent":"claude-code","worker":{"agent":"codex"}}}`, `{"Config":{"worker":{"agent":"codex"}}}`, true},
		{"different role", `{"Config":{"workerAgent":"codex"}}`, `{"Config":{"worker":{"agent":"cursor"}}}`, false},
		{"different model", `{"Config":{"worker":{"agent":"codex","agentConfig":{"model":"a"}}}}`, `{"Config":{"worker":{"agent":"codex","agentConfig":{"model":"b"}}}}`, false},
		{"different extension", `{"Config":{"extension":1}}`, `{"Config":{"extension":2}}`, false},
		{"different name", `{"DisplayName":"a","Config":null}`, `{"DisplayName":"b","Config":{}}`, false},
		{"different repository", `{"GitHubRepositoryID":1,"Config":null}`, `{"GitHubRepositoryID":2,"Config":{}}`, false},
		{"scratch config", `{"config":{"workerAgent":"codex","source":"scratch"}}`, `{"config":{"worker":{"agent":"codex"},"source":"scratch"}}`, true},
		{"invalid config", `{"Config":[]}`, `{"Config":{}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := "Config"
			if test.name == "scratch config" {
				key = "config"
			}
			if got := projectCreatePayloadEqual([]byte(test.stored), []byte(test.retry), key); got != test.equal {
				t.Fatalf("equal=%v, want %v", got, test.equal)
			}
		})
	}
}

func TestNewProjectCreateRetryKeepsLaterSettings(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	key := uuid.NewString()
	input := domain.CreateProject{
		DisplayName: "New", RepositoryURL: "https://example.test/" + key, DefaultBranch: "main",
		Config: json.RawMessage(`{"workerAgent":"codex","extension":"keep"}`),
	}
	project, err := store.CreateProject(ctx, principal, fixture.orgID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	canonical := json.RawMessage(`{"worker":{"agent":"codex"},"extension":"keep"}`)
	if !jsonEqual(project.Config, canonical) {
		t.Fatalf("new project config is not canonical: %s", project.Config)
	}
	updated, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, project.ID, domain.ProjectSettingsPatch{
		Config: json.RawMessage(`{"worker":{"agentConfig":{"model":"later-model"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []json.RawMessage{input.Config, canonical} {
		input.Config = config
		retry, err := store.CreateProject(ctx, principal, fixture.orgID, key, input)
		if err != nil || retry.ID != project.ID || !jsonEqual(retry.Config, updated.Config) {
			t.Fatalf("retry changed the project/settings: %+v, %v", retry, err)
		}
	}
	input.DisplayName = "Different"
	if _, err := store.CreateProject(ctx, principal, fixture.orgID, key, input); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed identity accepted: %v", err)
	}
	var persisted json.RawMessage
	if err := admin.QueryRow(ctx, `SELECT payload->'Config' FROM ao_commands WHERE org_id = $1 AND idempotency_key = $2`, fixture.orgID, key).Scan(&persisted); err != nil || !jsonEqual(persisted, canonical) {
		t.Fatalf("new command config=%s err=%v", persisted, err)
	}
}

func TestLegacyProjectCreateRetriesSurviveConfigNormalization(t *testing.T) {
	for _, kind := range []string{"project.create", "github.project.create", "github.scratch.create"} {
		for _, config := range []json.RawMessage{nil, json.RawMessage(`{"workerAgent":"codex","orchestratorAgent":"claude-code","extension":{"keep":true}}`)} {
			t.Run(kind+"/"+string(config), func(t *testing.T) {
				store, admin, fixture := openNotificationTestStore(t)
				ctx := context.Background()
				principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
				key := uuid.NewString()
				var input any
				var create func(json.RawMessage) (domain.Project, error)
				switch kind {
				case "project.create":
					request := domain.CreateProject{DisplayName: "Legacy", RepositoryURL: "https://example.test/legacy", DefaultBranch: "main", Config: config}
					input = request
					create = func(raw json.RawMessage) (domain.Project, error) {
						request.Config = raw
						return store.CreateProject(ctx, principal, fixture.orgID, key, request)
					}
				case "github.project.create":
					request := domain.CreateGitHubProject{GitHubRepositoryID: 123, DisplayName: "Legacy", Config: config}
					input = request
					create = func(raw json.RawMessage) (domain.Project, error) {
						request.Config = raw
						return store.CreateGitHubProject(ctx, principal, fixture.orgID, key, request)
					}
				case "github.scratch.create":
					request := domain.CreateGitHubScratchProject{DisplayName: "Legacy", Config: config}
					// Scratch creation supplied this default before settings normalization.
					if len(config) == 0 {
						config = json.RawMessage(`{"source":"scratch"}`)
					}
					input = map[string]any{
						"repositoryId": int64(0), "installationId": int64(0), "authorityUserExternalId": "",
						"authorityEnvironment": "", "capabilityHash": []byte(nil), "displayName": request.DisplayName,
						"config": config, "session": request.Session,
					}
					create = func(raw json.RawMessage) (domain.Project, error) {
						request.Config = raw
						project, _, err := store.CreateGitHubScratchProject(ctx, principal, fixture.orgID, key, 10, request)
						return project, err
					}
				}
				payload, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := admin.Exec(ctx, `INSERT INTO ao_commands (org_id, idempotency_key, kind, payload, status, result)
					VALUES ($1, $2, $3, $4, 'succeeded', jsonb_build_object('projectId', $5::text, 'sessionId', $6::text))`,
					fixture.orgID, key, kind, payload, fixture.projectID, fixture.sessionID); err != nil {
					t.Fatal(err)
				}
				project, err := create(config)
				if err != nil || project.ID != fixture.projectID {
					t.Fatalf("unchanged legacy retry: project=%s err=%v", project.ID, err)
				}
				canonical, err := domain.NormalizeProjectConfig(config)
				if err != nil {
					t.Fatal(err)
				}
				if project, err := create(canonical); err != nil || project.ID != fixture.projectID {
					t.Fatalf("equivalent canonical retry: project=%s err=%v", project.ID, err)
				}
				if _, err := create(json.RawMessage(`{"worker":{"agent":"cursor"}}`)); !errors.Is(err, ErrIdempotencyMismatch) {
					t.Fatalf("changed config accepted: %v", err)
				}
				var stored json.RawMessage
				if err := admin.QueryRow(ctx, `SELECT payload FROM ao_commands WHERE org_id = $1 AND idempotency_key = $2`, fixture.orgID, key).Scan(&stored); err != nil || !jsonEqual(stored, payload) {
					t.Fatalf("historical payload changed: %s err=%v", stored, err)
				}
			})
		}
	}
}
