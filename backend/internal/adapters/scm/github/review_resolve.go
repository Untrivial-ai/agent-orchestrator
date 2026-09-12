package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.SCMReviewResolver = (*Provider)(nil)

// ResolveReviewThread marks a GitHub pull-request review thread as resolved.
func (p *Provider) ResolveReviewThread(ctx context.Context, request ports.SCMReviewResolveRequest) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("github scm: review resolver is not configured")
	}
	threadID := strings.TrimSpace(request.ThreadID)
	if threadID == "" {
		return fmt.Errorf("github scm: review thread id is required")
	}
	_, err := p.client.doGraphQL(ctx, `mutation ResolveReviewThread($threadId: ID!) {
  resolveReviewThread(input: { threadId: $threadId }) { thread { id isResolved } }
}`, map[string]any{"threadId": threadID})
	return err
}
