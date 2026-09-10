package multi

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// NamedMerger pairs a routing key with a merger. The Key must match the
// string that the provider's ParseRepository sets in SCMRepo.Provider (e.g.
// "github", "gitlab").
type NamedMerger struct {
	Key    string
	Merger ports.SCMMerger
}

// Merger routes MergePullRequest calls to the correct sub-provider based on
// SCMPRRef.Repo.Provider. This mirrors the dispatch pattern used by Provider
// for SCM observation, allowing PR merge actions to target both GitHub and
// GitLab through a single ports.SCMMerger instance.
type Merger struct {
	byName map[string]ports.SCMMerger
}

// NewMerger creates a Merger from one or more named sub-providers.
func NewMerger(mergers ...NamedMerger) *Merger {
	m := &Merger{
		byName: make(map[string]ports.SCMMerger, len(mergers)),
	}
	for _, nm := range mergers {
		m.byName[nm.Key] = nm.Merger
	}
	return m
}

func (m *Merger) resolve(key string) (ports.SCMMerger, error) {
	if p, ok := m.byName[key]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("scm multi merger: unknown provider %q", key)
}

// MergePullRequest delegates to the sub-provider matching request.PR.Repo.Provider.
// An unknown provider key returns a clear error rather than nil-dereferencing.
func (m *Merger) MergePullRequest(ctx context.Context, request ports.SCMMergeRequest) (ports.SCMMergeResult, error) {
	p, err := m.resolve(request.PR.Repo.Provider)
	if err != nil {
		return ports.SCMMergeResult{}, err
	}
	return p.MergePullRequest(ctx, request)
}
