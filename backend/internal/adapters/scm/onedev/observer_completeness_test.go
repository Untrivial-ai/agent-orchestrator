package onedev

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestListPRsByRepoRejectsTruncatedSnapshot(t *testing.T) {
	total := maxPaginationPages*maxPageCount + 1
	p, repo, _ := newObserverProvider(t, "nested/project", func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		page := []restPullRequest{}
		for i := offset; i < min(offset+count, total); i++ {
			page = append(page, restPullRequest{ID: int64(i + 1), Number: i + 1, Status: "OPEN"})
		}
		writeJSON(t, w, page)
	})
	got, err := p.ListPRsByRepo(context.Background(), repo, time.Time{})
	if err == nil || got != nil {
		t.Fatalf("truncated discovery returned %d PRs, error %v; want error without snapshot", len(got), err)
	}
}

func TestFetchCIFullWindowIsPartial(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			builds := make([]restBuild, prBuildsPageCount)
			for i := range builds {
				builds[i] = restBuild{ID: int64(i + 1), Number: i + 1, JobName: fmt.Sprint(i), Status: "SUCCESSFUL", CommitHash: "current"}
				if stale && i > 0 {
					builds[i].CommitHash = "old"
				}
			}
			p, repo, _ := newObserverProvider(t, "project", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, builds) })
			client, err := p.clientForRepo(repo)
			if err != nil {
				t.Fatal(err)
			}
			host, err := p.hostForRepo(repo)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.fetchCI(context.Background(), client, host, repo, &restPullRequest{Number: 1, BuildCommitHash: "current"}, "current")
			if err != nil {
				t.Fatal(err)
			}
			if !got.Partial || got.Summary != string(domain.CIUnknown) {
				t.Fatalf("bounded window = %+v; want partial unknown", got)
			}
		})
	}
}
