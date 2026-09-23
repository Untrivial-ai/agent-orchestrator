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
	data, err := p.client.doGraphQL(ctx, `mutation ResolveReviewThread($threadId: ID!) {
  resolveReviewThread(input: { threadId: $threadId }) { thread { id isResolved } }
}`, map[string]any{"threadId": threadID})
	if err != nil {
		return err
	}
	mutation, ok := data["resolveReviewThread"].(map[string]any)
	if !ok {
		return fmt.Errorf("github scm: resolve review thread returned no mutation payload")
	}
	thread, ok := mutation["thread"].(map[string]any)
	returnedThreadID, _ := thread["id"].(string)
	if !ok || strings.TrimSpace(returnedThreadID) == "" {
		return fmt.Errorf("github scm: resolve review thread returned no thread")
	}
	if returnedThreadID != threadID {
		return fmt.Errorf("github scm: resolve review thread returned unexpected thread %q", returnedThreadID)
	}
	resolved, ok := thread["isResolved"].(bool)
	if !ok || !resolved {
		return fmt.Errorf("github scm: resolve review thread was not confirmed resolved")
	}
	return nil
}
