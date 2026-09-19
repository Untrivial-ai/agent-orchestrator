// Package authutil contains bounded, injectable helpers for local authentication
// evidence. Credential presence never establishes remote authorization.
package authutil

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Evidence carries status and a fixed source label, never a credential or path.
// Callers must use Authoritative or NoAuthEvidence for definitive results.
type Evidence struct {
	Status         ports.AgentAuthStatus
	Source         string
	authoritative  bool
	explicitNoAuth bool
}

// Authoritative records a documented validation or rejection. The source must
// be a constant label, not command output, user input, a path, or a secret.
func Authoritative(status ports.AgentAuthStatus, source string) Evidence {
	if status != ports.AgentAuthStatusAuthorized && status != ports.AgentAuthStatusUnauthorized {
		return Evidence{Status: ports.AgentAuthStatusUnknown}
	}
	return Evidence{Status: status, Source: source, authoritative: true}
}

// NoAuthEvidence requires an explicitly selected provider documented as no-auth.
func NoAuthEvidence(selected bool) Evidence {
	if !selected {
		return Evidence{Status: ports.AgentAuthStatusUnknown}
	}
	return Evidence{Status: ports.AgentAuthStatus("not_applicable"), Source: "selected-provider", explicitNoAuth: true}
}

// FirstDefinitive selects the strongest evidence, preserving input order on ties.
// Successful validation wins over rejection; authoritative rejection wins over
// local configuration. Unmarked definitive states cannot establish either.
func FirstDefinitive(evidence ...Evidence) Evidence {
	best := Evidence{Status: ports.AgentAuthStatusUnknown}
	bestRank := 0
	for _, item := range evidence {
		rank := 0
		switch {
		case item.Status == ports.AgentAuthStatusAuthorized && item.authoritative:
			rank = 5
		case item.Status == ports.AgentAuthStatusUnauthorized && item.authoritative:
			rank = 4
		case item.Status == ports.AgentAuthStatus("not_applicable") && item.explicitNoAuth:
			rank = 3
		case item.Status == ports.AgentAuthStatus("configured"):
			rank = 2
		}
		if rank > bestRank {
			best, bestRank = item, rank
		}
	}
	return best
}

// ExpiryEvidence is for trustworthy expiry fields whose units/schema were
// established by the adapter. Expired credentials with a native refresh path
// remain configured; expiry alone says nothing about remote refresh success.
func ExpiryEvidence(expires time.Time, refreshable bool, now time.Time) Evidence {
	if expires.IsZero() {
		return Evidence{Status: ports.AgentAuthStatusUnknown}
	}
	if !expires.After(now) && !refreshable {
		return Authoritative(ports.AgentAuthStatusUnauthorized, "credential-expiry")
	}
	return Evidence{Status: ports.AgentAuthStatus("configured"), Source: "credential-expiry"}
}

// ParseExpiry accepts positive Unix seconds or RFC3339 timestamps. Adapters
// with millisecond fields must convert explicitly; units are never guessed.
func ParseExpiry(value any) (time.Time, bool) {
	var seconds float64
	switch value := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed, true
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return time.Time{}, false
		}
		seconds = parsed
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return time.Time{}, false
		}
		seconds = parsed
	case float64:
		seconds = value
	case int64:
		seconds = float64(value)
	case int:
		seconds = float64(value)
	default:
		return time.Time{}, false
	}
	if seconds <= 0 || seconds >= float64(math.MaxInt64) || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0), true
}
