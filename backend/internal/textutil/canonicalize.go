package textutil

import "strings"

// ConversationFactBytes is the maximum byte length for canonicalized conversation facts.
// This is the single source of truth for the 16 KiB bound used by both
// session_manager and workflow dedup.
const ConversationFactBytes = 16 << 10

// CanonicalizeConversationFact normalizes a conversation fact for storage and dedup.
// Algorithm: strings.TrimSpace, then truncate to ConversationFactBytes with
// strings.ToValidUTF8 repair on multibyte boundaries.
//
// This is the single canonical implementation. session_manager.boundedConversationFact
// and workflow.CanonicalizePrompt both delegate here.
func CanonicalizeConversationFact(value string) string {
	value = strings.TrimSpace(value)
	if ConversationFactBytes > 0 && len(value) > ConversationFactBytes {
		return strings.ToValidUTF8(value[:ConversationFactBytes], "�")
	}
	return value
}
