package gitlab

import (
	scmgitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
)

// ErrNoToken re-exports the SCM provider's canonical sentinel so the
// tracker and SCM adapter share one error identity. Callers that need to
// distinguish "no token" from other failures should use
// errors.Is(err, scmgitlab.ErrNoToken) regardless of whether the failure
// originated in the tracker or the SCM provider.
var ErrNoToken = scmgitlab.ErrNoToken
