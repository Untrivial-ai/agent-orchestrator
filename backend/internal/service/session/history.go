package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type HistoryFilter struct {
	ProjectID domain.ProjectID
	Kind      domain.SessionKind
	Delivery  string
	Query     string
	Cursor    string
	Since     *time.Time
	Limit     int
}

type HistoryItem struct {
	Session              domain.Session
	StoppedAt            *time.Time
	WorkspaceDisposition domain.WorkspaceDisposition
	CleanupFailureCode   string
	RetentionHolds       []string
}

type HistoryPage struct {
	Sessions   []HistoryItem
	NextCursor string
}

type historyReader interface {
	ListSessionHistory(context.Context, domain.SessionHistoryFilter) ([]domain.SessionHistoryEntry, error)
}

type historyCleanupReader interface {
	GetSessionCleanupFacts(context.Context, domain.SessionID) (domain.SessionCleanupRecord, bool, error)
}

// History returns stopped sessions as a bounded, stable keyset page. The store
// selects identities before the more expensive PR/review enrichment runs.
func (s *Service) History(ctx context.Context, filter HistoryFilter) (HistoryPage, error) {
	reader, ok := s.store.(historyReader)
	if !ok {
		return HistoryPage{}, fmt.Errorf("session history store is unavailable")
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 || len(filter.Query) > 100 || len(filter.Cursor) > 256 ||
		(filter.Delivery != "" && filter.Delivery != "no_pr" && filter.Delivery != "merged" && filter.Delivery != "open" && filter.Delivery != "closed_unmerged") {
		return HistoryPage{}, fmt.Errorf("invalid history query")
	}
	storeFilter := domain.SessionHistoryFilter{ProjectID: filter.ProjectID, Kind: filter.Kind, Delivery: filter.Delivery, Query: filter.Query, Limit: filter.Limit}
	if filter.Since != nil {
		storeFilter.SinceEpoch = filter.Since.Unix()
	}
	if filter.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("invalid history cursor")
		}
		epochText, id, found := strings.Cut(string(decoded), "/")
		epoch, err := strconv.ParseInt(epochText, 10, 64)
		if !found || err != nil || epoch < 1 || id == "" || len(id) > 128 || strings.Contains(id, "/") {
			return HistoryPage{}, fmt.Errorf("invalid history cursor")
		}
		storeFilter.BeforeEpoch = epoch
		storeFilter.BeforeID = domain.SessionID(id)
	}
	entries, err := reader.ListSessionHistory(ctx, storeFilter)
	if err != nil {
		return HistoryPage{}, err
	}
	hasMore := len(entries) > filter.Limit
	if hasMore {
		entries = entries[:filter.Limit]
	}
	ids := make([]domain.SessionID, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	prs, err := s.store.ListPRFactsForSessions(ctx, ids)
	if err != nil {
		return HistoryPage{}, fmt.Errorf("list history PR facts: %w", err)
	}
	runs, err := s.store.ListCurrentHeadReviewRunsForSessions(ctx, ids)
	if err != nil {
		return HistoryPage{}, fmt.Errorf("list history review runs: %w", err)
	}
	page := HistoryPage{Sessions: make([]HistoryItem, 0, len(entries))}
	assessedAt := s.now().UTC()
	for _, entry := range entries {
		rec, exists, err := s.store.GetSession(ctx, entry.ID)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("get history session %s: %w", entry.ID, err)
		}
		if !exists || !rec.IsTerminated {
			continue // A concurrent restore moved this session out of History.
		}
		_, activeSwitch, err := s.store.GetActiveAgentSwitch(ctx, entry.ID)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("get history agent switch %s: %w", entry.ID, err)
		}
		sess, err := s.toSessionWithFacts(rec, prs[entry.ID], runs[entry.ID])
		if err != nil {
			return HistoryPage{}, err
		}
		item := HistoryItem{Session: sess, StoppedAt: entry.StoppedAt, RetentionHolds: []string{}}
		if reader, ok := s.store.(historyCleanupReader); ok {
			facts, exists, err := reader.GetSessionCleanupFacts(ctx, entry.ID)
			if err != nil {
				return HistoryPage{}, fmt.Errorf("get history cleanup facts %s: %w", entry.ID, err)
			}
			if exists && facts.SessionGeneration == rec.CleanupGeneration {
				item.WorkspaceDisposition = facts.WorkspaceDisposition
				item.CleanupFailureCode = facts.FailureCode
			}
		}
		item.RetentionHolds = retentionHolds(rec, entry.StoppedAt, item.WorkspaceDisposition, prs[entry.ID], activeSwitch, assessedAt)
		page.Sessions = append(page.Sessions, item)
	}
	if hasMore && len(entries) > 0 {
		last := entries[len(entries)-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d/%s", last.SortEpoch, last.ID)))
	}
	return page, nil
}

// retentionHolds is a report-only assessment. It never authorizes deletion:
// even a row with no listed hold still requires a verified export and backup.
func retentionHolds(rec domain.SessionRecord, stoppedAt *time.Time, disposition domain.WorkspaceDisposition, prs []domain.PRFacts, activeSwitch bool, now time.Time) []string {
	holds := make([]string, 0, 6)
	if rec.Kind != domain.KindWorker {
		holds = append(holds, "orchestrator_excluded")
	}
	if rec.IsPinned {
		holds = append(holds, "pinned")
	}
	if stoppedAt == nil {
		holds = append(holds, "stop_time_unknown")
	} else if stoppedAt.After(now.AddDate(0, 0, -180)) {
		holds = append(holds, "within_retention_window")
	}
	if disposition != domain.DispositionRemoved && disposition != domain.DispositionNotApplicable {
		holds = append(holds, "workspace_not_reclaimed")
	}
	if activeSwitch {
		holds = append(holds, "agent_switch_active")
	}
	if len(prs) == 0 {
		holds = append(holds, "delivery_unverified")
	} else {
		for _, pr := range prs {
			if !pr.Merged {
				holds = append(holds, "pr_not_merged")
				break
			}
		}
	}
	// No backup/export proof is stored in AO v0.13.0. Fail closed until a
	// dedicated retention lifecycle records it durably.
	holds = append(holds, "backup_unverified")
	return holds
}
