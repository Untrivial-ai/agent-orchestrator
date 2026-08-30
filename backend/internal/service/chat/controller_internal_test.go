package chat

import (
	"context"
	"testing"
	"time"
)

func TestRequiredNativeHistoryUsesHandoffStartupBudget(t *testing.T) {
	ctx, cancel := nativeHistoryContext(context.Background(), true)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("required native history context has no deadline")
	}
	if remaining := time.Until(deadline); remaining < 2*nativeHistorySettleLimit {
		t.Fatalf("required native history budget = %s, want at least %s",
			remaining, 2*nativeHistorySettleLimit)
	}
}
