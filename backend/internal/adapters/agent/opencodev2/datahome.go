package opencodev2

import "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"

// DataHome returns the XDG_DATA_HOME OpenCode 2 is launched with; its database
// and sessions live under <data home>/opencode-v2-home/opencode.
func DataHome() (string, bool) { return opencode.V2DataHome() }
