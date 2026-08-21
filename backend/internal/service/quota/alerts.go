package quota

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TransitionAlerts emits alerts only when a previously observed account crosses
// an actionable boundary. Initial discovery does not generate historical noise.
func TransitionAlerts(previous, next domain.QuotaSnapshot) []domain.QuotaAlert {
	if previous.Provider == "" || previous.Provider != next.Provider || previous.AccountID != next.AccountID {
		return nil
	}
	oldLimits := make(map[string]domain.QuotaLimit, len(previous.Limits))
	for _, limit := range previous.Limits {
		oldLimits[limitKey(limit)] = limit
	}
	var alerts []domain.QuotaAlert
	for _, limit := range next.Limits {
		old, ok := oldLimits[limitKey(limit)]
		if !ok {
			continue
		}
		oldSeverity, newSeverity := LimitSeverity(old), LimitSeverity(limit)
		if limit.Category == domain.QuotaSpendLimit && !reached(old) && reached(limit) {
			alerts = append(alerts, newAlert(next, limit.ID, "spend_reached", SeverityExhausted,
				fmt.Sprintf("%s spend control reached", providerLabel(next.Provider)),
				fmt.Sprintf("%s is not currently available for additional spend.", limitLabel(limit))))
			continue
		}
		if oldSeverity == SeverityExhausted && newSeverity != SeverityExhausted {
			alerts = append(alerts, newAlert(next, limit.ID, "available", newSeverity,
				fmt.Sprintf("%s usage is available again", providerLabel(next.Provider)),
				alertBody(limit)))
			continue
		}
		if severityRank(newSeverity) >= severityRank(SeverityWarning) && severityRank(newSeverity) > severityRank(oldSeverity) {
			alerts = append(alerts, newAlert(next, limit.ID, "threshold", newSeverity,
				fmt.Sprintf("%s usage is %s", providerLabel(next.Provider), newSeverity),
				alertBody(limit)))
		}
	}

	oldBalances := make(map[string]domain.QuotaBalance, len(previous.Balances))
	for _, balance := range previous.Balances {
		oldBalances[balance.ID] = balance
	}
	for _, balance := range next.Balances {
		old, ok := oldBalances[balance.ID]
		if !ok || old.Unlimited || balance.Unlimited || !positiveBalance(old.Value) || positiveBalance(balance.Value) {
			continue
		}
		alerts = append(alerts, newAlert(next, domain.QuotaLimitID(balance.ID), "credits_depleted", SeverityExhausted,
			fmt.Sprintf("%s credits depleted", providerLabel(next.Provider)),
			fmt.Sprintf("%s has no reported credits remaining.", balanceLabel(balance))))
	}
	return alerts
}

func newAlert(snapshot domain.QuotaSnapshot, limitID domain.QuotaLimitID, kind string, severity Severity, title, body string) domain.QuotaAlert {
	createdAt := snapshot.ObservedAt.UTC()
	raw := strings.Join([]string{string(snapshot.Provider), string(snapshot.AccountID), string(limitID), kind, string(severity), createdAt.Format(time.RFC3339Nano)}, "\x00")
	id := fmt.Sprintf("quota_%x", sha256.Sum256([]byte(raw)))[:30]
	return domain.QuotaAlert{ID: id, Provider: snapshot.Provider, AccountID: snapshot.AccountID, LimitID: limitID, Kind: kind, Severity: string(severity), Title: title, Body: body, CreatedAt: createdAt}
}

func alertBody(limit domain.QuotaLimit) string {
	label := limitLabel(limit)
	if remaining := limit.RemainingPercent(); remaining != nil {
		return fmt.Sprintf("%s has %.0f%% remaining.", label, *remaining)
	}
	return fmt.Sprintf("%s changed state.", label)
}

func reached(limit domain.QuotaLimit) bool { return limit.Reached != nil && *limit.Reached }

func severityRank(value Severity) int {
	return map[Severity]int{SeverityUnknown: 0, SeverityNormal: 1, SeverityWarning: 2, SeverityCritical: 3, SeverityExhausted: 4}[value]
}

func positiveBalance(value string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && parsed > 0
}

func providerLabel(provider domain.QuotaProviderID) string {
	value := strings.ReplaceAll(strings.ReplaceAll(string(provider), "_", " "), "-", " ")
	if value == "" {
		return "Provider"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func limitLabel(limit domain.QuotaLimit) string {
	if strings.TrimSpace(limit.Name) != "" {
		return limit.Name
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(limit.ID), "_", " "), "-", " ")
}

func balanceLabel(balance domain.QuotaBalance) string {
	if strings.TrimSpace(balance.Name) != "" {
		return balance.Name
	}
	return balance.ID
}
