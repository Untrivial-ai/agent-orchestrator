package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// UpsertQuotaSnapshot atomically writes the account and every reported bucket.
// Complete snapshots replace the provider inventory; partial updates merge.
func (s *Store) UpsertQuotaSnapshot(ctx context.Context, input domain.QuotaSnapshot) error {
	return s.PersistQuotaObservation(ctx, input, nil)
}

// PersistQuotaObservation writes the merged account snapshot and any alerts
// derived from its state transition in one transaction.
func (s *Store) PersistQuotaObservation(ctx context.Context, input domain.QuotaSnapshot, alerts []domain.QuotaAlert) error {
	snapshot := domain.NormalizeQuotaSnapshot(input)
	if snapshot.Provider == "" {
		return fmt.Errorf("quota provider is required")
	}
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("quota observation time is required")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "upsert quota snapshot", func(q *gen.Queries) error {
		if err := q.UpsertQuotaAccount(ctx, quotaAccountParams(snapshot)); err != nil {
			return err
		}
		account := gen.DeleteQuotaLimitsForAccountParams{Provider: string(snapshot.Provider), AccountID: string(snapshot.AccountID)}
		if snapshot.Completeness == domain.QuotaComplete {
			if err := q.DeleteQuotaLimitsForAccount(ctx, account); err != nil {
				return err
			}
			if err := q.DeleteQuotaBalancesForAccount(ctx, gen.DeleteQuotaBalancesForAccountParams(account)); err != nil {
				return err
			}
		}
		for _, limit := range snapshot.Limits {
			if limit.ID == "" {
				continue
			}
			params := quotaLimitParams(snapshot, limit)
			if err := q.UpsertQuotaLimit(ctx, params); err != nil {
				return err
			}
			if _, err := q.InsertQuotaHistoryIfChanged(ctx, quotaHistoryParams(snapshot, limit)); err != nil {
				return err
			}
		}
		for _, balance := range snapshot.Balances {
			if balance.ID == "" {
				continue
			}
			if err := q.UpsertQuotaBalance(ctx, quotaBalanceParams(snapshot, balance)); err != nil {
				return err
			}
		}
		for _, alert := range alerts {
			if _, err := q.InsertQuotaAlert(ctx, gen.InsertQuotaAlertParams{
				ID: alert.ID, Provider: string(alert.Provider), AccountID: string(alert.AccountID),
				LimitID: string(alert.LimitID), Kind: alert.Kind, Severity: alert.Severity,
				Title: alert.Title, Body: alert.Body, CreatedAt: alert.CreatedAt,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListQuotaSnapshots returns the latest snapshot for every provider account.
func (s *Store) ListQuotaSnapshots(ctx context.Context) ([]domain.QuotaSnapshot, error) {
	accounts, err := s.qr.ListQuotaAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list quota accounts: %w", err)
	}
	out := make([]domain.QuotaSnapshot, 0, len(accounts))
	for _, account := range accounts {
		snapshot, err := s.quotaSnapshotFromAccount(ctx, account)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	return out, nil
}

// GetQuotaSnapshot returns the latest snapshot for one provider account.
func (s *Store) GetQuotaSnapshot(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID) (domain.QuotaSnapshot, bool, error) {
	row, err := s.qr.GetQuotaAccount(ctx, gen.GetQuotaAccountParams{Provider: string(provider), AccountID: string(accountID)})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.QuotaSnapshot{}, false, nil
	}
	if err != nil {
		return domain.QuotaSnapshot{}, false, fmt.Errorf("get quota account: %w", err)
	}
	snapshot, err := s.quotaSnapshotFromAccount(ctx, row)
	return snapshot, err == nil, err
}

func (s *Store) quotaSnapshotFromAccount(ctx context.Context, row gen.QuotaAccount) (domain.QuotaSnapshot, error) {
	key := gen.ListQuotaLimitsForAccountParams{Provider: row.Provider, AccountID: row.AccountID}
	limits, err := s.qr.ListQuotaLimitsForAccount(ctx, key)
	if err != nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("list quota limits: %w", err)
	}
	balances, err := s.qr.ListQuotaBalancesForAccount(ctx, gen.ListQuotaBalancesForAccountParams(key))
	if err != nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("list quota balances: %w", err)
	}
	snapshot := domain.QuotaSnapshot{
		Provider: domain.QuotaProviderID(row.Provider), AccountID: domain.QuotaAccountID(row.AccountID),
		AccountLabel: row.AccountLabel, PlanType: row.PlanType, AuthMode: row.AuthMode,
		Capabilities: domain.QuotaCapabilities{
			SupportsRead: row.SupportsRead != 0, SupportsSubscribe: row.SupportsSubscribe != 0,
			SupportsHistory: row.SupportsHistory != 0, SupportsCredits: row.SupportsCredits != 0,
			SupportsSpendLimits: row.SupportsSpendLimits != 0,
		},
		Completeness: domain.QuotaCompleteness(row.Completeness), ObservedAt: row.ObservedAt,
	}
	for _, limit := range limits {
		snapshot.Limits = append(snapshot.Limits, quotaLimitFromGen(limit))
	}
	for _, balance := range balances {
		snapshot.Balances = append(snapshot.Balances, quotaBalanceFromGen(balance))
	}
	snapshot.RefreshError = row.LastRefreshError
	return snapshot, nil
}

// RecordQuotaRefreshFailure stores the latest on-demand refresh error for an account.
func (s *Store) RecordQuotaRefreshFailure(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID, message string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.RecordQuotaRefreshFailure(ctx, gen.RecordQuotaRefreshFailureParams{
		LastRefreshError: message,
		Provider:         string(provider),
		AccountID:        string(accountID),
	}); err != nil {
		return fmt.Errorf("record quota refresh failure: %w", err)
	}
	return nil
}

// ListQuotaHistory returns recent observations for one provider account.
func (s *Store) ListQuotaHistory(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID, since time.Time, limit int64) ([]domain.QuotaHistoryPoint, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.qr.ListQuotaHistoryForAccount(ctx, gen.ListQuotaHistoryForAccountParams{
		Provider: string(provider), AccountID: string(accountID), ObservedAt: since, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list quota history: %w", err)
	}
	out := make([]domain.QuotaHistoryPoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.QuotaHistoryPoint{
			LimitID: domain.QuotaLimitID(row.LimitID), WindowType: row.WindowType,
			Scope: domain.QuotaLimitScope(row.Scope), ScopeID: row.ScopeID,
			UsedPercent: nullFloatPtr(row.UsedPercent), ResetsAt: nullTimeToTimePtr(row.ResetsAt),
			Reached: nullIntBoolPtr(row.Reached), ObservedAt: row.ObservedAt,
		})
	}
	return out, nil
}

// ListQuotaAlerts returns recent quota threshold alerts.
func (s *Store) ListQuotaAlerts(ctx context.Context, since time.Time, limit int64) ([]domain.QuotaAlert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.qr.ListQuotaAlerts(ctx, gen.ListQuotaAlertsParams{Since: since, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("list quota alerts: %w", err)
	}
	out := make([]domain.QuotaAlert, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.QuotaAlert{
			ID: row.ID, Provider: domain.QuotaProviderID(row.Provider), AccountID: domain.QuotaAccountID(row.AccountID),
			LimitID: domain.QuotaLimitID(row.LimitID), Kind: row.Kind, Severity: row.Severity,
			Title: row.Title, Body: row.Body, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// DeleteQuotaHistoryBefore removes observations older than before.
func (s *Store) DeleteQuotaHistoryBefore(ctx context.Context, before time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	count, err := s.qw.DeleteQuotaHistoryBefore(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("delete quota history: %w", err)
	}
	return count, nil
}

// CompactQuotaHistory bounds account-level history while preserving useful
// resolution: one point per minute for 24 hours, per hour for 30 days, and per
// day for 90 days. Older points are deleted.
func (s *Store) CompactQuotaHistory(ctx context.Context, now time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var deleted int64
	err := s.inTx(ctx, "compact quota history", func(q *gen.Queries) error {
		deleteBefore := now.Add(-90 * 24 * time.Hour)
		count, err := q.DeleteQuotaHistoryBefore(ctx, deleteBefore)
		if err != nil {
			return err
		}
		deleted += count

		dailyBefore := now.Add(-30 * 24 * time.Hour)
		count, err = q.CompactQuotaHistoryDaily(ctx, gen.CompactQuotaHistoryDailyParams{RangeStart: deleteBefore, RangeEnd: dailyBefore})
		if err != nil {
			return err
		}
		deleted += count

		hourlyBefore := now.Add(-24 * time.Hour)
		count, err = q.CompactQuotaHistoryHourly(ctx, gen.CompactQuotaHistoryHourlyParams{RangeStart: dailyBefore, RangeEnd: hourlyBefore})
		if err != nil {
			return err
		}
		deleted += count

		count, err = q.CompactQuotaHistoryMinute(ctx, gen.CompactQuotaHistoryMinuteParams{RangeStart: hourlyBefore, RangeEnd: now})
		if err != nil {
			return err
		}
		deleted += count
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

func quotaAccountParams(s domain.QuotaSnapshot) gen.UpsertQuotaAccountParams {
	boolInt := func(value bool) int64 {
		if value {
			return 1
		}
		return 0
	}
	return gen.UpsertQuotaAccountParams{
		Provider: string(s.Provider), AccountID: string(s.AccountID), AccountLabel: s.AccountLabel,
		PlanType: s.PlanType, AuthMode: s.AuthMode,
		SupportsRead: boolInt(s.Capabilities.SupportsRead), SupportsSubscribe: boolInt(s.Capabilities.SupportsSubscribe),
		SupportsHistory: boolInt(s.Capabilities.SupportsHistory), SupportsCredits: boolInt(s.Capabilities.SupportsCredits),
		SupportsSpendLimits: boolInt(s.Capabilities.SupportsSpendLimits), Completeness: string(s.Completeness), ObservedAt: s.ObservedAt,
	}
}

func quotaLimitParams(s domain.QuotaSnapshot, l domain.QuotaLimit) gen.UpsertQuotaLimitParams {
	observedAt := l.ObservedAt
	if observedAt.IsZero() {
		observedAt = s.ObservedAt
	}
	return gen.UpsertQuotaLimitParams{
		Provider: string(s.Provider), AccountID: string(s.AccountID), LimitID: string(l.ID), WindowType: l.WindowType,
		Category: string(l.Category), Scope: string(l.Scope), ScopeID: l.ScopeID, LimitName: l.Name,
		UsedPercent: floatPtrToNull(l.UsedPercent), RemainingValue: floatPtrToNull(l.RemainingValue),
		TotalValue: floatPtrToNull(l.TotalValue), Unit: l.Unit, WindowDurationSeconds: durationPtrToNull(l.WindowDuration),
		ResetsAt: timePtrToNullTime(l.ResetsAt), Reached: boolPtrToNullInt(l.Reached), ReachedReason: l.ReachedReason,
		ObservedAt: observedAt,
	}
}

func quotaHistoryParams(s domain.QuotaSnapshot, l domain.QuotaLimit) gen.InsertQuotaHistoryIfChangedParams {
	p := quotaLimitParams(s, l)
	return gen.InsertQuotaHistoryIfChangedParams{
		InputProvider: p.Provider, InputAccountID: p.AccountID, InputLimitID: p.LimitID,
		InputWindowType: p.WindowType, InputScope: p.Scope, InputScopeID: p.ScopeID,
		InputUsedPercent: p.UsedPercent, InputResetsAt: p.ResetsAt, InputReached: p.Reached,
		InputObservedAt: p.ObservedAt,
	}
}

func quotaBalanceParams(s domain.QuotaSnapshot, b domain.QuotaBalance) gen.UpsertQuotaBalanceParams {
	observedAt := b.ObservedAt
	if observedAt.IsZero() {
		observedAt = s.ObservedAt
	}
	unlimited := int64(0)
	if b.Unlimited {
		unlimited = 1
	}
	return gen.UpsertQuotaBalanceParams{Provider: string(s.Provider), AccountID: string(s.AccountID), BalanceID: b.ID, BalanceName: b.Name, Value: b.Value, Currency: b.Currency, Unlimited: unlimited, ObservedAt: observedAt}
}

func quotaLimitFromGen(row gen.QuotaLimit) domain.QuotaLimit {
	return domain.QuotaLimit{
		ID: domain.QuotaLimitID(row.LimitID), Name: row.LimitName, Category: domain.QuotaLimitCategory(row.Category),
		Scope: domain.QuotaLimitScope(row.Scope), ScopeID: row.ScopeID, UsedPercent: nullFloatPtr(row.UsedPercent),
		RemainingValue: nullFloatPtr(row.RemainingValue), TotalValue: nullFloatPtr(row.TotalValue), Unit: row.Unit,
		WindowType: row.WindowType, WindowDuration: nullIntDurationPtr(row.WindowDurationSeconds),
		ResetsAt: nullTimeToTimePtr(row.ResetsAt), Reached: nullIntBoolPtr(row.Reached), ReachedReason: row.ReachedReason,
		ObservedAt: row.ObservedAt,
	}
}

func quotaBalanceFromGen(row gen.QuotaBalance) domain.QuotaBalance {
	return domain.QuotaBalance{ID: row.BalanceID, Name: row.BalanceName, Value: row.Value, Currency: row.Currency, Unlimited: row.Unlimited != 0, ObservedAt: row.ObservedAt}
}

func floatPtrToNull(v *float64) sql.NullFloat64 {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}
func nullFloatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	value := v.Float64
	return &value
}
func durationPtrToNull(v *time.Duration) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v / time.Second), Valid: true}
}
func nullIntDurationPtr(v sql.NullInt64) *time.Duration {
	if !v.Valid {
		return nil
	}
	value := time.Duration(v.Int64) * time.Second
	return &value
}
func boolPtrToNullInt(v *bool) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	if *v {
		return sql.NullInt64{Int64: 1, Valid: true}
	}
	return sql.NullInt64{Int64: 0, Valid: true}
}
func nullIntBoolPtr(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	value := v.Int64 != 0
	return &value
}
