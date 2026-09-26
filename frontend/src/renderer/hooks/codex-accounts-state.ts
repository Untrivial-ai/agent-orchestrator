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
	const presentedActiveID = incoming.deviceReconciliation?.status === "verified" && incoming.deviceReconciliation.activeAccountVerified
		? incoming.activeAccountId
		: undefined;
	const normalized = accounts.map((account) => ({
		...account,
		// Device ownership is intentionally not preserved across an unverified
		// reconciliation response. A stale In use badge is more misleading than the
		// short local-check transition.
		active: account.id === presentedActiveID,
	}));
	normalized.sort((left, right) => {
		// Keep row positions stable across activation changes so switching only
		// moves the "In use" badge — never reshuffles the list.
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

// Switching installs an already-saved local credential. A temporary provider
// or network failure must not make that credential unusable; only a definitive
// signed-out state requires login first.
export function codexAccountCanSwitch(account: Pick<CodexAccount, "status">): boolean {
	return account.status === "valid";
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
	global_account_changed: "settings.codexAccounts.reason.globalAccountChanged",
	login_pending: "settings.codexAccounts.reason.loginPending",
	login_completed: "settings.codexAccounts.reason.loginCompleted",
	login_cancelled: "settings.codexAccounts.reason.loginCancelled",
	login_failed: "settings.codexAccounts.reason.loginFailed",
	login_unauthorized: "settings.codexAccounts.reason.loginUnauthorized",
	login_expired: "settings.codexAccounts.reason.loginExpired",
	switch_state_unavailable: "settings.codexAccounts.reason.switchStateUnavailable",
} as const;

export const codexAccountReasonCodes = Object.keys(reasonKeys) as Array<keyof typeof reasonKeys>;

export type CodexAccountMessageKey = (typeof reasonKeys)[keyof typeof reasonKeys]
	| "settings.codexAccounts.switch.requested"
	| "settings.codexAccounts.switch.completed"
	| "settings.codexAccounts.switch.failed"
	| "settings.codexAccounts.switch.unknown";

export function codexAccountReasonKey(reasonCode: string | null | undefined): CodexAccountMessageKey {
	return reasonKeys[reasonCode as keyof typeof reasonKeys] ?? "settings.codexAccounts.reason.unknown";
}

export type CodexSwitchDisplay = {
	key: CodexAccountMessageKey;
	tone: "muted" | "warning" | "error";
	busy: boolean;
	mutationBlocked: boolean;
};

export function codexSwitchDisplay(switchState: CodexAccountSwitch): CodexSwitchDisplay {
	const phase = switchState.phase;
	const terminal = phase === "completed" || phase === "failed";
	const busy = !terminal;
	let key: CodexAccountMessageKey;
	if (busy) {
		key = "settings.codexAccounts.switch.requested";
	} else if (phase === "completed") {
		key = "settings.codexAccounts.switch.completed";
	} else if (phase === "failed") {
		key = "settings.codexAccounts.switch.failed";
	} else {
		key = "settings.codexAccounts.switch.unknown";
	}
	return {
		key,
		tone: phase === "failed" ? "error" : "muted",
		busy,
		mutationBlocked: phase !== "completed" && phase !== "failed",
	};
}
