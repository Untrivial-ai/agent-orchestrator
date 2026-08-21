package ports

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var ErrQuotaRefreshUnsupported = errors.New("provider quota cannot be refreshed on demand")

// QuotaSink is the provider-neutral write boundary. Chat adapters translate
// provider payloads before calling it; storage and UI never inspect raw frames.
type QuotaSink interface {
	RecordQuotaSnapshot(context.Context, domain.QuotaSnapshot) error
}

// QuotaReadFunc performs one provider-native account quota read. Keeping the
// callback here lets the collector coordinate reads without knowing any adapter.
type QuotaReadFunc func(context.Context) (ChatRateLimits, error)

// QuotaCollector owns account-level read coalescing and durable observations.
// Controllers use it instead of multiplying provider calls per session.
type QuotaCollector interface {
	QuotaSink
	CollectRateLimits(context.Context, domain.QuotaProviderID, domain.QuotaAccountID, QuotaReadFunc) (ChatRateLimits, error)
}

// ChatQuotaIdentity is implemented by readable providers that can declare the
// account key before a provider request is made.
type ChatQuotaIdentity interface {
	QuotaIdentity() (domain.QuotaProviderID, domain.QuotaAccountID)
}

// QuotaReader is implemented by providers that can return an authoritative
// snapshot on demand. Push-only adapters publish partial snapshots instead.
type QuotaReader interface {
	ReadQuota(context.Context) (domain.QuotaSnapshot, error)
}
