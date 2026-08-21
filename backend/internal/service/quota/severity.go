package quota

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// Severity is the provider-neutral urgency of a quota state.
type Severity string

const (
	// SeverityNormal means the provider quota is comfortably available.
	SeverityNormal Severity = "normal"
	// SeverityWarning means the provider quota is approaching exhaustion.
	SeverityWarning Severity = "warning"
	// SeverityCritical means the provider quota is close to exhaustion.
	SeverityCritical Severity = "critical"
	// SeverityExhausted means the provider quota is fully consumed or blocked.
	SeverityExhausted Severity = "exhausted"
	// SeverityUnknown means the provider did not report enough data to derive urgency.
	SeverityUnknown Severity = "unknown"
)

// LimitSeverity derives urgency from a single provider-reported quota limit.
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

// SnapshotSeverity returns the most urgent severity across a snapshot's limits.
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
