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
		active: Boolean(incoming.deviceReconciliation?.activeAccountVerified) && account.id === incoming.activeAccountId,
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

export type CodexAuthenticationDisplay = {
	key:
		| "settings.codexAccounts.signedIn"
		| "settings.codexAccounts.signedOut"
		| "settings.codexAccounts.reason.authUnauthorized"
		| "settings.codexAccounts.authenticationChecking"
		| "settings.codexAccounts.authenticationCheckFailed"
		| "settings.codexAccounts.authenticationCheckTimeout"
		| "settings.codexAccounts.authenticationUpdateRequired"
		| "settings.codexAccounts.authenticationCodexNotInstalled";
	action: "retry" | "reauthenticate" | null;
	checking: boolean;
};

export function codexAuthenticationDisplay(account: Pick<CodexAccount, "authentication" | "status">): CodexAuthenticationDisplay {
	const authentication = account.authentication;
	if (account.status === "signed_out") {
		return { key: "settings.codexAccounts.signedOut", action: "reauthenticate", checking: false };
	}
	if (authentication.freshness === "checking") {
		return { key: "settings.codexAccounts.authenticationChecking", action: null, checking: true };
	}
	if (authentication.state === "unauthorized") {
		return { key: "settings.codexAccounts.reason.authUnauthorized", action: "reauthenticate", checking: false };
	}
	switch (authentication.reasonCode) {
		case "auth_skipped_not_installed":
			return { key: "settings.codexAccounts.authenticationCodexNotInstalled", action: null, checking: false };
		case "auth_check_unsupported":
			return { key: "settings.codexAccounts.authenticationUpdateRequired", action: null, checking: false };
		case "auth_check_timeout":
			return { key: "settings.codexAccounts.authenticationCheckTimeout", action: "retry", checking: false };
		case "auth_check_failed":
		case "auth_check_inconclusive":
			return { key: "settings.codexAccounts.authenticationCheckFailed", action: "retry", checking: false };
	}
	if (codexAccountAuthorized(account)) {
		return { key: "settings.codexAccounts.signedIn", action: null, checking: false };
	}
	if (authentication.reasonCode === "not_checked") {
		return { key: "settings.codexAccounts.authenticationChecking", action: null, checking: true };
	}
	return { key: "settings.codexAccounts.authenticationCheckFailed", action: "retry", checking: false };
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
	capacity_client_start_failed: "settings.codexAccounts.reason.capacityClientStartFailed",
	capacity_provider_rejected: "settings.codexAccounts.reason.capacityProviderRejected",
	capacity_provider_unavailable: "settings.codexAccounts.reason.capacityProviderUnavailable",
	capacity_check_stopped: "settings.codexAccounts.reason.capacityCheckStopped",
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
	| `settings.codexAccounts.switch.${CodexAccountSwitch["phase"] | "unknown" | "sessions_recovery_required"}`;

export function codexAccountReasonKey(reasonCode: string | null | undefined): CodexAccountMessageKey {
	return reasonKeys[reasonCode as keyof typeof reasonKeys] ?? "settings.codexAccounts.reason.unknown";
}

export type CodexSwitchDisplay = {
	key: CodexAccountMessageKey;
	tone: "muted" | "warning" | "error";
	busy: boolean;
	mutationBlocked: boolean;
	canRecover: boolean;
	recoveryKind: "sessions" | "account" | null;
};

const sessionRecoveryFailureCodes = new Set([
	"restart_unconfirmed",
	"reviewer_restart_unconfirmed",
]);

export function codexSwitchDisplay(switchState: CodexAccountSwitch): CodexSwitchDisplay {
	const phase = switchState.phase;
	const canRecover = switchState.canRecover && (phase === "rollback_required" || phase === "recovery_required");
	const terminal = phase === "completed" || phase === "failed" || phase === "recovery_required" || (phase === "rollback_required" && canRecover);
	const busy = !terminal;
	const recoveryKind = canRecover && switchState.restartRunningSessions && sessionRecoveryFailureCodes.has(switchState.failureCode ?? "")
		? "sessions"
		: canRecover
			? "account"
			: null;
	let key: CodexAccountMessageKey;
	if (busy) {
		key = phase === "rollback_required"
			? "settings.codexAccounts.switch.rollback_required"
			: "settings.codexAccounts.switch.requested";
	} else if (recoveryKind === "sessions") {
		key = "settings.codexAccounts.switch.sessions_recovery_required";
	} else if (canRecover) {
		key = "settings.codexAccounts.switch.recovery_required";
	} else if (phase === "completed") {
		key = "settings.codexAccounts.switch.completed";
	} else if (phase === "failed") {
		key = "settings.codexAccounts.switch.failed";
	} else {
		key = "settings.codexAccounts.switch.unknown";
	}
	return {
		key,
		tone: phase === "failed" ? "error" : phase === "rollback_required" || phase === "recovery_required" ? "warning" : "muted",
		busy,
		mutationBlocked: phase !== "completed" && phase !== "failed",
		canRecover,
		recoveryKind,
	};
}
