package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestProviderAccountStorageStartsEmptyAndReturnsWhatWasSaved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	initial, err := s.LoadProviderAccounts(ctx)
	if err != nil || len(initial.Accounts) != 0 || len(initial.Routes) != 0 || len(initial.Defaults) != 0 {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	want := domain.ProviderAccountState{
		Accounts: []domain.ProviderAccount{
			{ID: "alice", Provider: "codex", DisplayName: "Cedar Codex", Email: "alice@example.com", Kind: "oauth", CredentialRef: "alice.json", AuthID: "auth-alice"},
			{ID: "key", Provider: "claude", DisplayName: "Pine Claude", Email: "work key", Kind: "api_key"},
		},
		Defaults:         map[string]string{"codex": "alice", "claude": ""},
		Routes:           []domain.ProviderSessionRoute{{SessionID: "s1", Provider: "codex", AccountID: "alice"}, {SessionID: "s2", Provider: "claude"}},
		NativeImports:    map[string]domain.NativeProviderImport{"codex": {Fingerprint: "f1", AccountID: "alice", Email: "alice@example.com"}},
		NativeKeyImports: map[string]domain.NativeProviderImport{"claude": {Fingerprint: "f2"}},
	}
	if err = s.SaveProviderAccounts(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadProviderAccounts(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestProviderAccountRouteChangesInvalidateOnlyTheSessionsInvolved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "account-cdc")
	var ids []domain.SessionID
	for range 3 {
		session, err := s.CreateSession(ctx, sampleRecord("account-cdc"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, session.ID)
	}
	first, second, unrelated := ids[0], ids[1], ids[2]
	invalidated := func(after int64) map[domain.SessionID]bool {
		t.Helper()
		events, err := s.EventsAfter(ctx, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[domain.SessionID]bool{}
		for _, event := range events {
			var payload map[string]string
			if err := json.Unmarshal(event.Payload, &payload); err != nil || string(event.Type) != "session_updated" || len(payload) != 1 {
				t.Fatalf("event=%+v payload=%v err=%v", event, payload, err)
			}
			seen[domain.SessionID(payload["id"])] = true
		}
		return seen
	}
	baseline, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.ProviderAccountState{Routes: []domain.ProviderSessionRoute{{SessionID: first, Provider: "codex"}, {SessionID: second, Provider: "codex"}}}
	if err = s.SaveProviderAccounts(ctx, state); err != nil {
		t.Fatal(err)
	}
	if seen := invalidated(baseline); len(seen) != 2 || !seen[first] || !seen[second] {
		t.Fatalf("new routes invalidated %v", seen)
	}
	baseline, _ = s.LatestSeq(ctx)
	if err = s.SaveProviderAccounts(ctx, state); err != nil {
		t.Fatal(err)
	}
	if seen := invalidated(baseline); len(seen) != 0 {
		t.Fatalf("an unchanged save invalidated %v", seen)
	}
	state.Routes = state.Routes[1:]
	if err = s.SaveProviderAccounts(ctx, state); err != nil {
		t.Fatal(err)
	}
	if seen := invalidated(baseline); !seen[first] || !seen[second] || seen[unrelated] {
		t.Fatalf("a dropped route invalidated %v", seen)
	}
}
