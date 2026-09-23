import { resetLabel } from "./conversationChrome";
import type { McpServer } from "./types";

/**
 * Words for the status bars above a conversation, shaped like the desktop's
 * `ChatStatusBanners`: a short headline that says what is wrong, and at most one
 * quiet line of detail. The provider's raw error text is left out — on a phone
 * it was a paragraph that pushed the conversation off screen.
 *
 * Every banner carries a `key` built from what it is reporting. Closing a banner
 * hides that key for the visit; when the underlying state changes — another
 * server fails, the quota crosses into critical — the key changes and the banner
 * comes back, because that is news.
 */
export type BannerTone = "warning" | "danger" | "muted";

export type BannerCopy = {
	key: string;
	title: string;
	body?: string;
};

export function mcpBanner(servers: readonly McpServer[], reloadError?: string): BannerCopy | undefined {
	if (servers.length === 0) return undefined;
	const names = servers.map((server) => server.failureReason ? `${server.name} · ${server.failureReason}` : server.name);
	return {
		key: `mcp:${servers.map((server) => `${server.name}/${server.status}`).sort().join(",")}`,
		title: servers.length === 1 ? "A tool server did not start" : `${servers.length} tool servers did not start`,
		body: reloadError ? `Reload failed: ${reloadError}` : `${names.join(", ")}. The agent works around them silently.`,
	};
}

export function reauthBanner(reauthRequiredAt: string, command?: string): BannerCopy {
	return {
		key: `reauth:${reauthRequiredAt}`,
		title: "Sign in again to keep going",
		body: command ? `Run “${command}” on the AO host, then send again.` : "Sign in with the agent's CLI on the AO host, then send again.",
	};
}

export function threadBanner(status: string | undefined): BannerCopy | undefined {
	if (status === "system_error") {
		return { key: "thread:system_error", title: "The agent's thread hit an internal error", body: "New turns will usually fail. The conversation and worktree are kept." };
	}
	if (status === "closed") {
		return { key: "thread:closed", title: "The agent closed this thread", body: "AO kept the history, but the agent no longer holds it." };
	}
	return undefined;
}

export function controllerStoppedBanner(terminated: boolean, error?: string): BannerCopy {
	return terminated
		? { key: "stopped:terminated", title: "This session is terminated", body: "Its conversation and worktree are preserved." }
		: { key: `stopped:${error ?? ""}`, title: "The agent is stopped", body: error };
}

export function quotaBanner(quota: { percent: number; severity: "warn" | "critical"; resetsInSeconds?: number }): BannerCopy {
	const resets = resetLabel(quota.resetsInSeconds);
	return {
		// Keyed on severity, not the exact percent: creeping from 81% to 82% is not
		// news, crossing into critical is.
		key: `quota:${quota.severity}`,
		title: `${quota.percent}% of quota used`,
		body: resets ? `Resets in ${resets}.` : undefined,
	};
}

export function rolledBackBanner(count: number): BannerCopy | undefined {
	if (count === 0) return undefined;
	return {
		key: `rolledback:${count}`,
		title: count === 1 ? "1 turn was rolled back" : `${count} turns were rolled back`,
		body: `The agent no longer remembers ${count === 1 ? "it" : "them"}.`,
	};
}

export function errorBanner(kind: "load" | "action", message: string): BannerCopy {
	return {
		key: `${kind}:${message}`,
		title: kind === "load" ? "Couldn't refresh the conversation" : "That didn't work",
		body: message,
	};
}
