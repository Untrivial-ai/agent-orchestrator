package workflow

import "github.com/aoagents/agent-orchestrator/backend/internal/textutil"

// CanonicalizePrompt normalizes a prompt for dedup comparison.
// Delegates to the single canonical implementation in textutil.
// Both session_manager.Send and workflow retry dedup use the same function.
var CanonicalizePrompt = textutil.CanonicalizeConversationFact
