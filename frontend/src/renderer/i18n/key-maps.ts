import type { AppShortcutId, ShortcutCategory } from "../../shared/shortcuts";
import type { components } from "../../api/schema";
import type { MessageKey } from "./messages";

/** Exhaustive mappings keep dynamic domain identifiers inside the typed catalog. */
export const shortcutLabelKeys: Record<AppShortcutId, MessageKey> = {
	"new-session": "shortcut.new-session",
	"new-shell-terminal": "shortcut.new-shell-terminal",
	"close-shell-terminal": "shortcut.close-shell-terminal",
	"keyboard-shortcuts": "shortcut.keyboard-shortcuts",
	"command-palette": "shortcut.command-palette",
	"open-settings": "shortcut.open-settings",
	"toggle-sidebar": "shortcut.toggle-sidebar",
	"open-project": "shortcut.open-project",
	"previous-session": "shortcut.previous-session",
	"next-session": "shortcut.next-session",
	"previous-tab": "shortcut.previous-tab",
	"next-tab": "shortcut.next-tab",
	"toggle-inspector": "shortcut.toggle-inspector",
	"focus-terminal": "shortcut.focus-terminal",
	"toggle-browser-devtools": "titlebar.devtools",
};

export const shortcutCategoryLabelKeys: Record<ShortcutCategory, MessageKey> = {
	General: "shortcut.category.general",
	Navigation: "shortcut.category.navigation",
	Session: "shortcut.category.session",
};

export type AgentSwitchErrorCode = NonNullable<components["schemas"]["AgentSwitch"]["errorCode"]>;

/** Exhaustive known-code mapping with a separate runtime fallback for newer daemons. */
export const agentSwitchErrorLabelKeys: Record<AgentSwitchErrorCode, MessageKey> = {
	daemon_restart_pre_stop: "switchAgent.error.daemonRestartPreStop",
	daemon_restart_post_stop: "switchAgent.error.daemonRestartPostStop",
	daemon_restart_unrecoverable_target: "switchAgent.error.daemonRestartUnrecoverableTarget",
	daemon_restart_before_delivery: "switchAgent.error.daemonRestartBeforeDelivery",
	delivery_unconfirmed: "switchAgent.error.deliveryUnconfirmed",
	source_session_terminated: "switchAgent.error.sourceSessionTerminated",
	source_stop_unconfirmed: "switchAgent.error.sourceStopUnconfirmed",
	target_binary_missing: "switchAgent.error.targetBinaryMissing",
	target_agent_unauthorized: "switchAgent.error.targetAgentUnauthorized",
	request_cancelled: "switchAgent.error.requestCancelled",
	source_blocked: "switchAgent.error.sourceBlocked",
	failed_pre_stop: "switchAgent.error.failedPreStop",
	failed_post_stop: "switchAgent.error.failedPostStop",
	target_ready_failed: "switchAgent.error.targetReadyFailed",
	delivery_failed: "switchAgent.error.deliveryFailed",
	switch_failed: "switchAgent.error.switchFailed",
	target_start_unconfirmed: "switchAgent.error.targetStartUnconfirmed",
	source_restore_unconfirmed: "switchAgent.sourceRecovery.description",
};
