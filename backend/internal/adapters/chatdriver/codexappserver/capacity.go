package codexappserver

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type capacityWireWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *int64   `json:"windowDurationMins"`
	ResetsAt           *int64   `json:"resetsAt"`
}

type capacityWireBucket struct {
	LimitID              *string             `json:"limitId"`
	LimitName            *string             `json:"limitName"`
	PlanType             *string             `json:"planType"`
	Primary              *capacityWireWindow `json:"primary"`
	Secondary            *capacityWireWindow `json:"secondary"`
	RateLimitReachedType *string             `json:"rateLimitReachedType"`
	SpendControlReached  *bool               `json:"spendControlReached"`
}

type capacityReadEnvelope struct {
	RateLimits            capacityWireBucket                       `json:"rateLimits"`
	RateLimitsByLimitID   map[string]capacityWireBucket            `json:"rateLimitsByLimitId"`
	RateLimitResetCredits *codexproto.RateLimitResetCreditsSummary `json:"rateLimitResetCredits"`
}

func capacityObservationFromEnvelope(envelope capacityReadEnvelope, observedAt time.Time, partial bool) ports.CodexCapacityObservation {
	overall := normalizeCapacityBucket(envelope.RateLimits, "codex", partial)
	var plan *string
	if envelope.RateLimits.PlanType != nil {
		plan = safeCapacityText(*envelope.RateLimits.PlanType, 80)
	}
	additional := make([]domain.CodexCapacityBucket, 0, len(envelope.RateLimitsByLimitID))
	for key, wire := range envelope.RateLimitsByLimitID {
		bucket := normalizeCapacityBucket(wire, key, partial)
		if bucket == nil || (overall != nil && bucket.LimitID == overall.LimitID) {
			continue
		}
		additional = append(additional, *bucket)
	}
	sort.Slice(additional, func(i, j int) bool { return additional[i].LimitID < additional[j].LimitID })
	return ports.CodexCapacityObservation{
		Plan: plan, Overall: overall, AdditionalBuckets: additional,
		ResetCredits: normalizeResetCredits(envelope.RateLimitResetCredits),
		ObservedAt:   observedAt.UTC(), Partial: partial,
	}
}

func normalizeResetCredits(summary *codexproto.RateLimitResetCreditsSummary) *domain.CodexResetCreditsSummary {
	if summary == nil || summary.AvailableCount < 0 {
		return nil
	}
	result := &domain.CodexResetCreditsSummary{AvailableCount: summary.AvailableCount}
	for _, credit := range summary.Credits {
		if credit.Status != codexproto.RateLimitResetCreditStatusAvailable || credit.ExpiresAt == nil || *credit.ExpiresAt <= 0 {
			continue
		}
		expiresAt := time.Unix(*credit.ExpiresAt, 0).UTC()
		if result.NearestExpiresAt == nil || expiresAt.Before(*result.NearestExpiresAt) {
			result.NearestExpiresAt = &expiresAt
		}
	}
	return result
}

func normalizeCapacityBucket(wire capacityWireBucket, fallbackID string, partial bool) *domain.CodexCapacityBucket {
	id := strings.TrimSpace(fallbackID)
	if wire.LimitID != nil && strings.TrimSpace(*wire.LimitID) != "" {
		id = strings.TrimSpace(*wire.LimitID)
	}
	if safeCapacityText(id, 160) == nil {
		return nil
	}
	reached := domain.CodexCapacityNotReached
	if partial && wire.RateLimitReachedType == nil && wire.SpendControlReached == nil {
		reached = domain.CodexCapacityReachUnknown
	}
	if (wire.RateLimitReachedType != nil && strings.TrimSpace(*wire.RateLimitReachedType) != "") ||
		(wire.SpendControlReached != nil && *wire.SpendControlReached) {
		reached = domain.CodexCapacityReached
	}
	return &domain.CodexCapacityBucket{
		LimitID: id, DisplayName: safeOptionalCapacityText(wire.LimitName, 160),
		Primary: normalizeCapacityWindow(wire.Primary), Secondary: normalizeCapacityWindow(wire.Secondary),
		Reached: reached,
	}
}

func normalizeCapacityWindow(wire *capacityWireWindow) *domain.CodexCapacityWindow {
	if wire == nil || wire.UsedPercent == nil || math.IsNaN(*wire.UsedPercent) || math.IsInf(*wire.UsedPercent, 0) || *wire.UsedPercent < 0 || *wire.UsedPercent > 100 {
		return nil
	}
	window := &domain.CodexCapacityWindow{UsedPercent: *wire.UsedPercent}
	if wire.WindowDurationMins != nil && *wire.WindowDurationMins > 0 {
		value := *wire.WindowDurationMins
		window.WindowDurationMinutes = &value
	}
	if wire.ResetsAt != nil && *wire.ResetsAt > 0 {
		value := time.Unix(*wire.ResetsAt, 0).UTC()
		window.ResetsAt = &value
	}
	return window
}

func safeOptionalCapacityText(value *string, maxRunes int) *string {
	if value == nil {
		return nil
	}
	return safeCapacityText(*value, maxRunes)
}

func safeCapacityText(value string, maxRunes int) *string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return nil
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return nil
		}
	}
	return &value
}

func chatRateLimitsFromCapacity(observation ports.CodexCapacityObservation, now time.Time) ports.ChatRateLimits {
	limits := ports.ChatRateLimits{PrimaryUsedPercent: -1, SecondaryUsedPercent: -1}
	if observation.Plan != nil {
		limits.PlanLabel = *observation.Plan
	}
	if observation.Overall != nil {
		if observation.Overall.Primary != nil {
			limits.PrimaryUsedPercent = observation.Overall.Primary.UsedPercent
			limits.PrimaryResetsInSeconds = resetsAtIn(observation.Overall.Primary.ResetsAt, now)
		}
		if observation.Overall.Secondary != nil {
			limits.SecondaryUsedPercent = observation.Overall.Secondary.UsedPercent
			limits.SecondaryResetsInSeconds = resetsAtIn(observation.Overall.Secondary.ResetsAt, now)
		}
	}
	limits.CodexCapacity = &observation
	return limits
}

func resetsAtIn(resetsAt *time.Time, now time.Time) int64 {
	if resetsAt == nil {
		return 0
	}
	remaining := int64(resetsAt.Sub(now).Seconds())
	if remaining < 0 {
		return 0
	}
	return remaining
}
