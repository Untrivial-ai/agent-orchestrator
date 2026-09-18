package github

import (
	"errors"
	"testing"
	"time"
)

// rateLimitCooldownSource is the shape the provider-neutral observer requires
// (observe/scm/observer.go) to extract a cooldown from a rate-limit error via
// errors.As. Regression guard for the bug where *RateLimitError carried the
// hints as fields but not methods, so the observer's errors.As never matched
// for GitHub and cooldown never fired. A test double previously masked this.
type rateLimitCooldownSource interface {
	error
	GetRetryAfter() time.Duration
	GetResetAt() time.Time
}

func TestRateLimitErrorSatisfiesObserverCooldownContract(t *testing.T) {
	reset := time.Now().Add(90 * time.Second)
	var err error = &RateLimitError{ResetAt: reset, RetryAfter: 42 * time.Second, Message: "secondary limit"}

	var src rateLimitCooldownSource
	if !errors.As(err, &src) {
		t.Fatal("*github.RateLimitError must satisfy the observer's cooldown getter interface (errors.As failed)")
	}
	if got := src.GetRetryAfter(); got != 42*time.Second {
		t.Fatalf("GetRetryAfter = %v, want 42s", got)
	}
	if got := src.GetResetAt(); !got.Equal(reset) {
		t.Fatalf("GetResetAt = %v, want %v", got, reset)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("must still match ErrRateLimited")
	}
}
