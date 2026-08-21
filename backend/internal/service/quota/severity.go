package quota

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

type Severity string

const (
	SeverityNormal    Severity = "normal"
	SeverityWarning   Severity = "warning"
	SeverityCritical  Severity = "critical"
	SeverityExhausted Severity = "exhausted"
	SeverityUnknown   Severity = "unknown"
)

func LimitSeverity(limit domain.QuotaLimit) Severity {
	if limit.Reached != nil && *limit.Reached {
		return SeverityExhausted
	}
	if limit.UsedPercent == nil {
		return SeverityUnknown
	}
	switch {
	case *limit.UsedPercent >= 100:
		return SeverityExhausted
	case *limit.UsedPercent >= 90:
		return SeverityCritical
	case *limit.UsedPercent >= 75:
		return SeverityWarning
	default:
		return SeverityNormal
	}
}

func SnapshotSeverity(snapshot domain.QuotaSnapshot) Severity {
	worst := SeverityUnknown
	order := map[Severity]int{SeverityUnknown: 0, SeverityNormal: 1, SeverityWarning: 2, SeverityCritical: 3, SeverityExhausted: 4}
	for _, limit := range snapshot.Limits {
		candidate := LimitSeverity(limit)
		if order[candidate] > order[worst] {
			worst = candidate
		}
	}
	return worst
}
