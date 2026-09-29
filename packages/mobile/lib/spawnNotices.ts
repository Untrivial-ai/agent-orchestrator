// The warnings above the spawn controls, decided in one place so a single cause
// produces a single line. Pure, so it is unit-testable.

export const SPAWN_OFFLINE_NOTICE = "Not connected to your desktop. You can start a worker once it reconnects.";
export const NO_CHAT_AGENT_NOTICE =
	"No installed agent on this AO host currently supports Chat. Choose Terminal UI or install/authenticate a Chat-capable agent.";

export type SpawnNoticeInput = {
	/** The board's poll has lost the desktop. */
	offline: boolean;
	mode: "chat" | "tui";
	/** The agent catalog is still loading. */
	loading: boolean;
	/** The agent catalog answered (as opposed to still loading or failed). */
	catalogLoaded: boolean;
	catalogError: string | null;
	/** Agents usable in the current mode. */
	agentCount: number;
	modelError?: string;
};

export function spawnNotices(input: SpawnNoticeInput): string[] {
	// Every request on this screen fails the same way while the desktop is gone,
	// so one line says so instead of one per request.
	if (input.offline) return [SPAWN_OFFLINE_NOTICE];
	const notices: string[] = [];
	// Only a catalog that actually answered can say no agent supports Chat. A
	// failed load is empty too, and blaming the install for it misleads.
	if (input.mode === "chat" && !input.loading && input.catalogLoaded && !input.catalogError && input.agentCount === 0) {
		notices.push(NO_CHAT_AGENT_NOTICE);
	}
	if (input.catalogError) notices.push(input.catalogError);
	if (input.modelError) notices.push(input.modelError);
	return [...new Set(notices)];
}
