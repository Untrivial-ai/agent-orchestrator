package ports

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// AgentSwitchFailureAuthoritySnapshot is the validated, provider-neutral
// telemetry authority read by the filesystem adapter.
type AgentSwitchFailureAuthoritySnapshot struct {
	Present           bool
	EventsEnabled     bool
	ConsentGeneration string
}

// AgentSwitchFailureAuthorityReader isolates durable-authority I/O from policy
// coordination.
type AgentSwitchFailureAuthorityReader interface {
	ReadAgentSwitchFailureAuthority(context.Context) (AgentSwitchFailureAuthoritySnapshot, error)
}

// AgentSwitchFailureEncodedEvent is the opaque output of the provider adapter.
type AgentSwitchFailureEncodedEvent struct {
	EnvelopeEncodingVersion int
	Payload                 []byte
}

// AgentSwitchFailureEventEncoder keeps provider wire construction outside the
// domain and persistence layers.
type AgentSwitchFailureEventEncoder interface {
	EncodeAgentSwitchFailureEvent(domain.AgentSwitchEventBuildInput) (AgentSwitchFailureEncodedEvent, error)
}

// DeliveryOutcome is the complete provider-neutral result of one bounded
// delivery attempt. Cancellation values are local control decisions.
type DeliveryOutcome string

// DeliveryAccepted and the related constants enumerate provider-neutral
// delivery outcomes.
const (
	DeliveryAccepted          DeliveryOutcome = "accepted"
	DeliveryTransientFailure  DeliveryOutcome = "transient_failure"
	DeliveryPermanentFailure  DeliveryOutcome = "permanent_failure"
	DeliveryPolicyCancelled   DeliveryOutcome = "policy_cancelled"
	DeliveryShutdownCancelled DeliveryOutcome = "shutdown_cancelled"
)

// DeliveryErrorClass contains no provider response text or raw error.
type DeliveryErrorClass string

// DeliveryErrorNone and the related constants classify failures without
// retaining provider response text.
const (
	DeliveryErrorNone                DeliveryErrorClass = "none"
	DeliveryErrorNetwork             DeliveryErrorClass = "network"
	DeliveryErrorTimeout             DeliveryErrorClass = "timeout"
	DeliveryErrorRateLimited         DeliveryErrorClass = "rate_limited"
	DeliveryErrorProviderUnavailable DeliveryErrorClass = "provider_unavailable"
	DeliveryResponseLost             DeliveryErrorClass = "response_lost"
	DeliveryErrorInvalidPayload      DeliveryErrorClass = "invalid_payload"
	DeliveryErrorUnauthorized        DeliveryErrorClass = "unauthorized"
	DeliveryErrorUnsupportedEncoding DeliveryErrorClass = "unsupported_encoding"
	DeliveryErrorLocalInvariant      DeliveryErrorClass = "local_invariant"
)

// DeliveryThrottleScope identifies how broadly a provider response should
// delay subsequent attempts.
type DeliveryThrottleScope string

// DeliveryThrottleNone and the related constants enumerate supported throttle
// scopes.
const (
	DeliveryThrottleNone          DeliveryThrottleScope = "none"
	DeliveryThrottleErrorCategory DeliveryThrottleScope = "error_category"
	DeliveryThrottleAll           DeliveryThrottleScope = "all"
)

// DeliveryResult contains the bounded outcome and retry guidance from one
// delivery attempt.
type DeliveryResult struct {
	Outcome        DeliveryOutcome
	Class          DeliveryErrorClass
	RetryNotBefore time.Time
	ThrottleScope  DeliveryThrottleScope
}

// AgentSwitchFailureObserver receives immutable privacy-allowlisted bytes. It
// cannot read or mutate saga state and must acknowledge synchronously.
type AgentSwitchFailureObserver interface {
	ObserveAgentSwitchFailure(context.Context, domain.AgentSwitchFailureEvent) DeliveryResult
}

// AgentSwitchReportingPolicy supplies the current transaction-bound authority
// snapshot. Storage still performs the authoritative in-transaction check.
type AgentSwitchReportingPolicy interface {
	Authorization() domain.AgentSwitchReportingAuthorization
}
