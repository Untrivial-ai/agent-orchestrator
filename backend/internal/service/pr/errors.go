package pr

import "errors"

// Sentinel errors returned by the PR action service.
var (
	ErrInvalidPR        = errors.New("pr: invalid identity")
	ErrPRNotFound       = errors.New("pr: not found")
	ErrPRNotMergeable   = errors.New("pr: not mergeable")
	ErrPRHeadChanged    = errors.New("pr: head changed")
	ErrPRPreconditions  = errors.New("pr: merge preconditions unmet")
	ErrNothingToResolve = errors.New("pr: nothing to resolve")
	// ErrPRProviderUnavailable marks a failed provider refresh: the PR could
	// not be re-observed (network outage, rate limit, timeout), which is not
	// the same as the PR not existing and must not surface as "Unknown PR".
	ErrPRProviderUnavailable = errors.New("pr: provider unavailable")
)
