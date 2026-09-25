import type { QueryClient } from "@tanstack/react-query";

// Screen copies of a session the user is not looking at. The daemon still owns
// the session. Removing these queries is not termination, and parked terminals
// are not in this list: they live in the terminal cache, outside React Query.
const SESSION_COPY_PREFIXES = [
	"conversation",
	"conversation-models",
	"conversation-config-options",
	"conversation-skills",
	"editor-handoff",
	"session-interface-transition",
	"session-reviews",
	"session-source-file",
	"session-source-file-revision",
	"session-source-files",
	"session-workspace-diffs",
	"session-workspace-file",
	"session-workspace-file-revision",
	"session-workspace-files",
	"session-workspace-search",
	"session-workspace-tree",
	"workspace-file-paths",
] as const;

// The open session, plus the one just left, stay in memory so switching back
// does not wait on a refetch. Anything older is a cold copy.
const WARM_SESSION_COPIES = 1;

export function retainedSessionCopies(activeSessionId: string | undefined, previous: readonly string[]): string[] {
	const retained: string[] = [];
	if (activeSessionId) retained.push(activeSessionId);
	const limit = activeSessionId ? 1 + WARM_SESSION_COPIES : WARM_SESSION_COPIES;
	for (const sessionId of previous) {
		if (retained.length >= limit) break;
		if (sessionId && sessionId !== activeSessionId && !retained.includes(sessionId)) retained.push(sessionId);
	}
	return retained;
}

// Returns the sessions whose copies are still kept, most recently viewed first.
export function releaseColdSessionCopies(
	queryClient: QueryClient,
	activeSessionId: string | undefined,
	previous: readonly string[],
): string[] {
	const retained = retainedSessionCopies(activeSessionId, previous);
	const retainedSet = new Set(retained);
	for (const sessionId of previous) {
		if (!sessionId || retainedSet.has(sessionId)) continue;
		for (const prefix of SESSION_COPY_PREFIXES) {
			queryClient.removeQueries({ queryKey: [prefix, sessionId] });
		}
	}
	return retained;
}
