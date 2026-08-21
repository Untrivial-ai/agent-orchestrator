package quota

import (
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	// FreshWindow is how long a provider read is considered fresh.
	FreshWindow = 2 * time.Minute
	// AgingWindow is how long a provider read can age before it is stale.
	AgingWindow = 5 * time.Minute
)

// Merge combines a provider update with the last durable snapshot. Complete
// snapshots replace missing buckets; partial snapshots preserve them.
func Merge(current, update domain.QuotaSnapshot) domain.QuotaSnapshot {
	current = domain.NormalizeQuotaSnapshot(current)
	update = domain.NormalizeQuotaSnapshot(update)
	if current.Provider == "" || current.Provider != update.Provider || current.AccountID != update.AccountID {
		return update
	}
	if update.ObservedAt.Before(current.ObservedAt) {
		return current
	}

	merged := current
	merged.ObservedAt = update.ObservedAt
	merged.Completeness = update.Completeness
	if update.AccountLabel != "" {
		merged.AccountLabel = update.AccountLabel
	}
	if update.PlanType != "" {
		merged.PlanType = update.PlanType
	}
	if update.AuthMode != "" {
		merged.AuthMode = update.AuthMode
	}
	merged.Capabilities = mergeCapabilities(current.Capabilities, update.Capabilities)

	limits := make(map[string]domain.QuotaLimit)
	if update.Completeness != domain.QuotaComplete {
		for _, limit := range current.Limits {
			limits[limitKey(limit)] = limit
		}
	}
	for _, limit := range update.Limits {
		key := limitKey(limit)
		if previous, ok := limits[key]; ok {
			limit = mergeLimit(previous, limit)
		}
		limits[key] = limit
	}
	merged.Limits = make([]domain.QuotaLimit, 0, len(limits))
	for _, limit := range limits {
		merged.Limits = append(merged.Limits, limit)
	}
	sort.Slice(merged.Limits, func(i, j int) bool { return limitKey(merged.Limits[i]) < limitKey(merged.Limits[j]) })

	balances := make(map[string]domain.QuotaBalance)
	if update.Completeness != domain.QuotaComplete {
		for _, balance := range current.Balances {
			balances[balance.ID] = balance
		}
	}
	for _, balance := range update.Balances {
		balances[balance.ID] = balance
	}
	merged.Balances = make([]domain.QuotaBalance, 0, len(balances))
	for _, balance := range balances {
		merged.Balances = append(merged.Balances, balance)
	}
	sort.Slice(merged.Balances, func(i, j int) bool { return merged.Balances[i].ID < merged.Balances[j].ID })
	return merged
}

func mergeLimit(previous, update domain.QuotaLimit) domain.QuotaLimit {
	merged := previous
	if update.Name != "" {
		merged.Name = update.Name
	}
	if update.Category != "" {
		merged.Category = update.Category
	}
	if update.Scope != "" {
		merged.Scope = update.Scope
	}
	if update.ScopeID != "" {
		merged.ScopeID = update.ScopeID
	}
	if update.UsedPercent != nil {
		merged.UsedPercent = update.UsedPercent
	}
	if update.RemainingValue != nil {
		merged.RemainingValue = update.RemainingValue
	}
	if update.TotalValue != nil {
		merged.TotalValue = update.TotalValue
	}
	if update.Unit != "" {
		merged.Unit = update.Unit
	}
	if update.WindowDuration != nil {
		merged.WindowDuration = update.WindowDuration
	}
	if update.ResetsAt != nil {
		merged.ResetsAt = update.ResetsAt
	}
	if update.Reached != nil {
		merged.Reached = update.Reached
	}
	if update.ReachedReason != "" {
		merged.ReachedReason = update.ReachedReason
	}
	if !update.ObservedAt.IsZero() {
		merged.ObservedAt = update.ObservedAt
	}
	return merged
}

func limitKey(limit domain.QuotaLimit) string {
	return string(limit.ID) + "\x00" + limit.WindowType + "\x00" + string(limit.Scope) + "\x00" + limit.ScopeID
}

func mergeCapabilities(a, b domain.QuotaCapabilities) domain.QuotaCapabilities {
	return domain.QuotaCapabilities{
		SupportsRead:        a.SupportsRead || b.SupportsRead,
		SupportsSubscribe:   a.SupportsSubscribe || b.SupportsSubscribe,
		SupportsHistory:     a.SupportsHistory || b.SupportsHistory,
		SupportsCredits:     a.SupportsCredits || b.SupportsCredits,
		SupportsSpendLimits: a.SupportsSpendLimits || b.SupportsSpendLimits,
	}
}

// Freshness is derived at read time; it is never stored as durable state.
func Freshness(observedAt, now time.Time) string {
	if observedAt.IsZero() {
		return "unavailable"
	}
	age := now.Sub(observedAt)
	if age < 0 || age < FreshWindow {
		return "fresh"
	}
	if age <= AgingWindow {
		return "aging"
	}
	return "stale"
}
