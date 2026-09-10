import type { QueryClient } from "@tanstack/react-query";
import type { CodexAccount, CodexAccountSwitch, CodexAccountsResponse } from "./useCodexAccountsQuery";

export const codexAccountsQueryKey = ["codex-accounts"] as const;

export type AccountMergeMode = "replace" | "preserveMissing";

export function mergeCodexAccounts(
	current: CodexAccountsResponse | undefined,
	incoming: CodexAccountsResponse,
	mode: AccountMergeMode,
): CodexAccountsResponse {
	if (current && incoming.accountRevision < current.accountRevision) return current;
	const accounts = mode === "preserveMissing" && current
		? [...current.accounts.filter((account) => !incoming.accounts.some((next) => next.id === account.id)), ...incoming.accounts]
		: [...incoming.accounts];
	const normalized = accounts.map((account) => ({
		...account,
		active: account.id === incoming.activeAccountId,
	}));
	normalized.sort((left, right) => {
		if (left.active !== right.active) return left.active ? -1 : 1;
		return left.createdAt.localeCompare(right.createdAt) || left.id.localeCompare(right.id);
	});
	return { ...incoming, accounts: normalized };
}

export function writeCodexAccounts(
	queryClient: QueryClient,
	incoming: CodexAccountsResponse,
	mode: AccountMergeMode = "replace",
): void {
	queryClient.setQueryData<CodexAccountsResponse>(codexAccountsQueryKey, (current) =>
		mergeCodexAccounts(current, incoming, mode));
}

/**
 * A Codex account is only shown as signed in when its authentication
 * observation says so. The daemon establishes the active account's observation
 * with the same refresh-capable check the launch path uses, so an authorized
 * state here means the next Codex session can start.
 */
export function codexAccountAuthorized(account: Pick<CodexAccount, "authentication">): boolean {
	return account.authentication.state === "authorized" || account.authentication.state === "not_applicable";
}

export function codexAccountSignedOut(account: Pick<CodexAccount, "authentication" | "status">): boolean {
	return account.authentication.state === "unauthorized" || account.status === "signed_out";
}

const reasonKeys = {
	account_valid: "settings.codexAccounts.reason.accountValid",
	account_signed_out: "settings.codexAccounts.reason.accountSignedOut",
	account_descriptor_invalid: "settings.codexAccounts.reason.accountDescriptorInvalid",
	account_credential_home_missing: "settings.codexAccounts.reason.accountCredentialHomeMissing",
	account_unsafe_path: "settings.codexAccounts.reason.accountUnsafePath",
	authorized: "settings.codexAccounts.reason.authAuthorized",
	unauthorized: "settings.codexAccounts.reason.authUnauthorized",
	not_applicable: "settings.codexAccounts.reason.authNotApplicable",
	not_checked: "settings.codexAccounts.reason.authNotChecked",
	auth_check_failed: "settings.codexAccounts.reason.authCheckFailed",
	auth_check_inconclusive: "settings.codexAccounts.reason.authCheckInconclusive",
	auth_check_timeout: "settings.codexAccounts.reason.authCheckTimeout",
	auth_check_unsupported: "settings.codexAccounts.reason.authCheckUnsupported",
	auth_skipped_not_installed: "settings.codexAccounts.reason.authSkippedNotInstalled",
	capacity_available: "settings.codexAccounts.reason.capacityAvailable",
	capacity_near_limit: "settings.codexAccounts.reason.capacityNearLimit",
	capacity_exhausted: "settings.codexAccounts.reason.capacityExhausted",
	capacity_unsupported: "settings.codexAccounts.reason.capacityUnsupported",
	capacity_not_checked: "settings.codexAccounts.reason.capacityNotChecked",
	capacity_checking: "settings.codexAccounts.reason.capacityChecking",
	capacity_check_failed: "settings.codexAccounts.reason.capacityCheckFailed",
	capacity_check_inconclusive: "settings.codexAccounts.reason.capacityCheckInconclusive",
	capacity_check_timeout: "settings.codexAccounts.reason.capacityCheckTimeout",
	capacity_skipped_auth_unknown: "settings.codexAccounts.reason.capacitySkippedAuthUnknown",
	capacity_skipped_signed_out: "settings.codexAccounts.reason.capacitySkippedSignedOut",
	capacity_account_unavailable: "settings.codexAccounts.reason.capacityAccountUnavailable",
	capacity_invalidated: "settings.codexAccounts.reason.capacityInvalidated",
	supported: "settings.codexAccounts.reason.supported",
	unsupported: "settings.codexAccounts.reason.unsupported",
	unknown: "settings.codexAccounts.reason.unknown",
	global_credential_store_unsupported: "settings.codexAccounts.reason.globalCredentialStoreUnsupported",
	global_account_unverified: "settings.codexAccounts.reason.globalAccountUnverified",
	global_account_identity_unverified: "settings.codexAccounts.reason.globalAccountIdentityUnverified",
	global_account_changed: "settings.codexAccounts.reason.globalAccountChanged",
	login_pending: "settings.codexAccounts.reason.loginPending",
	login_completed: "settings.codexAccounts.reason.loginCompleted",
	login_cancelled: "settings.codexAccounts.reason.loginCancelled",
	login_failed: "settings.codexAccounts.reason.loginFailed",
	login_unauthorized: "settings.codexAccounts.reason.loginUnauthorized",
	login_unverified: "settings.codexAccounts.reason.loginUnverified",
	login_expired: "settings.codexAccounts.reason.loginExpired",
	running_session_not_resumable: "settings.codexAccounts.reason.runningSessionNotResumable",
	switch_state_unavailable: "settings.codexAccounts.reason.switchStateUnavailable",
	session_operation_in_progress: "settings.codexAccounts.reason.sessionOperationInProgress",
	stop_unconfirmed: "settings.codexAccounts.reason.stopUnconfirmed",
	activation_unconfirmed: "settings.codexAccounts.reason.activationUnconfirmed",
	restart_unconfirmed: "settings.codexAccounts.reason.restartUnconfirmed",
	rollback_unconfirmed: "settings.codexAccounts.reason.rollbackUnconfirmed",
	session_missing: "settings.codexAccounts.reason.sessionMissing",
	source_generation_changed: "settings.codexAccounts.reason.sourceGenerationChanged",
	reviewer_stop_unconfirmed: "settings.codexAccounts.reason.reviewerStopUnconfirmed",
	reviewer_restart_unconfirmed: "settings.codexAccounts.reason.reviewerRestartUnconfirmed",
	reviewer_native_history_changed: "settings.codexAccounts.reason.reviewerNativeHistoryChanged",
	daemon_restart_recovery: "settings.codexAccounts.reason.daemonRestartRecovery",
} as const;

export const codexAccountReasonCodes = Object.keys(reasonKeys) as Array<keyof typeof reasonKeys>;

export type CodexAccountMessageKey = (typeof reasonKeys)[keyof typeof reasonKeys]
	| `settings.codexAccounts.switch.${CodexAccountSwitch["phase"] | "unknown"}`;

export function codexAccountReasonKey(reasonCode: string | null | undefined): CodexAccountMessageKey {
	return reasonKeys[reasonCode as keyof typeof reasonKeys] ?? "settings.codexAccounts.reason.unknown";
}

export type CodexSwitchDisplay = {
	key: CodexAccountMessageKey;
	tone: "muted" | "warning" | "error";
	busy: boolean;
	mutationBlocked: boolean;
	canRecover: boolean;
};

export function codexSwitchDisplay(switchState: CodexAccountSwitch): CodexSwitchDisplay {
	const failureKey = switchState.failureCode ? codexAccountReasonKey(switchState.failureCode) : null;
	const failureKnown = failureKey !== "settings.codexAccounts.reason.unknown";
	const phase = switchState.phase;
	const canRecover = switchState.canRecover && (phase === "rollback_required" || phase === "recovery_required");
	const terminal = phase === "completed" || phase === "failed" || phase === "recovery_required" || (phase === "rollback_required" && canRecover);
	const busy = !terminal;
	const phaseKeys: Record<CodexAccountSwitch["phase"], CodexAccountMessageKey> = {
		requested: "settings.codexAccounts.switch.requested",
		stopping_sessions: "settings.codexAccounts.switch.stopping_sessions",
		sessions_stopped: "settings.codexAccounts.switch.sessions_stopped",
		checkpointing_source: "settings.codexAccounts.switch.checkpointing_source",
		activating_target: "settings.codexAccounts.switch.activating_target",
		verifying_target: "settings.codexAccounts.switch.verifying_target",
		restarting_sessions: "settings.codexAccounts.switch.restarting_sessions",
		rollback_required: "settings.codexAccounts.switch.rollback_required",
		recovery_required: "settings.codexAccounts.switch.recovery_required",
		completed: "settings.codexAccounts.switch.completed",
		failed: "settings.codexAccounts.switch.failed",
	};
	return {
		key: !busy && failureKnown && failureKey ? failureKey : phaseKeys[phase] ?? "settings.codexAccounts.switch.unknown",
		tone: phase === "failed" ? "error" : failureKnown || phase === "rollback_required" || phase === "recovery_required" ? "warning" : "muted",
		busy,
		mutationBlocked: phase !== "completed" && phase !== "failed",
		canRecover,
	};
}
