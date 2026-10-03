package session

import (
	"context"
	"fmt"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func benchmarkWorkspaceManifestService(b *testing.B) *Service {
	b.Helper()
	repo := newWorkspaceRepo(b)
	for index := 0; index < 500; index++ {
		writeWorkspaceFile(b, repo, fmt.Sprintf("src/group-%02d/file-%04d.txt", index%25, index), "baseline\n")
	}
	runGit(b, repo, "add", ".")
	runGit(b, repo, "commit", "-m", "benchmark inventory")
	for index := 0; index < 10; index++ {
		writeWorkspaceFile(b, repo, fmt.Sprintf("src/group-%02d/file-%04d.txt", index, index), "changed\n")
	}
	store := newFakeStore()
	store.sessions["bench"] = domain.SessionRecord{ID: "bench", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	return NewWithDeps(Deps{Store: store})
}

func BenchmarkWorkspaceManifestWarmRead(b *testing.B) {
	svc := benchmarkWorkspaceManifestService(b)
	if _, err := svc.GetWorkspaceManifest(context.Background(), "bench"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := svc.GetWorkspaceManifest(context.Background(), "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWorkspaceManifestRefresh(b *testing.B) {
	svc := benchmarkWorkspaceManifestService(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		svc.workspaceManifests.invalidateSession("bench")
		if _, err := svc.RefreshWorkspaceManifest(context.Background(), "bench"); err != nil {
			b.Fatal(err)
		}
	}
}
