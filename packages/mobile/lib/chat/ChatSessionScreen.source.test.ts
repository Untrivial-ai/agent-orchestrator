import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const screenSource = readFileSync(new URL("./ChatSessionScreen.tsx", import.meta.url), "utf8");
const composerSource = readFileSync(new URL("./ChatComposer.tsx", import.meta.url), "utf8");
const apiSource = readFileSync(new URL("./api.ts", import.meta.url), "utf8");

describe("active turn controls", () => {
	it("keeps working state in the conversation instead of a redundant status strip", () => {
		expect(screenSource).not.toContain("LiveTurnBar");
		expect(screenSource).not.toContain("Agent is working");
		expect(screenSource).not.toContain("stopTurn:");
	});

	it("uses one composer control for queued/running sends and interruption", () => {
		expect(composerSource).toContain("Boolean(activeTurn(snapshot))");
		expect(composerSource).toContain("composerPrimaryAction({ active, hasDraft, hasAttachments:");
		expect(composerSource).toContain("interrupting ? <ActivityIndicator");
	});

	it("docks queued messages above the composer with a per-message delete action", () => {
		expect(screenSource).toContain("onCancelQueuedTurn={conversation.cancelQueuedTurn}");
		expect(composerSource).toContain('accessibilityLabel={`Delete queued message: ${entry.message.text}`}');
	});

	it("promotes the exact queued turn when the user steers its dock row", () => {
		expect(apiSource).toContain('`/turns/${encodeURIComponent(turnId)}/steer`');
		expect(screenSource).toContain("onPromoteQueuedTurn={conversation.promoteQueuedTurn}");
		expect(composerSource).toContain('accessibilityLabel={`Steer queued message: ${entry.message.text}`}');
	});

	it("keeps the queued row compact without a second labeled steer pill", () => {
		expect(composerSource).not.toContain("queueSteerText");
		expect(composerSource).toContain('queueSteer: { width: 40, height: 40');
	});

	it("keeps settings and active-turn delivery state in one fixed-height row", () => {
		expect(composerSource).toContain("<View style={styles.metaRow}>");
		expect(composerSource).toContain('metaRow: { width: "100%", height: 44');
		expect(composerSource).toContain('numberOfLines={1} style={styles.deliveryNoteText}');
		expect(composerSource).toContain('Sent after this');
	});
	// One name per session, as on desktop: useWorkspaceQuery derives
	// `displayName ?? issueId ?? id`, ChatWorkspace prefers that over the
	// conversation's own title, and renaming anywhere is the same displayName
	// PATCH. With the precedence inverted, renaming on the board changed the row
	// and left this header showing the agent's auto-generated title.
	it("prefers the session's name over the conversation title", () => {
		expect(screenSource).toContain("const title = sessionName || conversation.snapshot?.title");
	});

	it("renames the session, not the conversation", () => {
		expect(screenSource).toContain("onRename: (next) => renameWorker(session.id, next)");
		expect(screenSource).not.toContain("conversation.rename(next)");
	});

	it("shows a failed start's reason instead of only a stopped-agent banner", () => {
		expect(screenSource).toContain('session.provisionState !== "failed"');
		expect(screenSource).toContain('title="Session failed to start" message={failedStart}');
		expect(screenSource).toContain('startFailure={failedStart}');
		expect(screenSource).toContain('disabled={interfaceTransitionActive || Boolean(failedStart)}');
	});
});
