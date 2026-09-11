import { describe, expect, it } from "vitest";
import type { CodexAccountsResponse } from "./useCodexAccountsQuery";
import { catalogFor } from "../i18n/messages";
import type { AppLocale } from "../i18n/locales";
import { codexAccountReasonCodes, codexAccountReasonKey, codexAuthenticationDisplay, codexSwitchDisplay, mergeCodexAccounts } from "./codex-accounts-state";
import type { CodexAccountSwitch } from "./useCodexAccountsQuery";

const account = (id: string, createdAt: string, active = false) => ({ id, createdAt, active });

function response(accounts: ReturnType<typeof account>[], activeAccountId = "b"): CodexAccountsResponse {
	return {
		accountRevision: 7,
		activeAccountId,
		accounts,
		capabilities: {},
		deviceReconciliation: { status: "verified", activeAccountVerified: true, reasonCode: "verified", retryable: false },
	} as CodexAccountsResponse;
}

describe("mergeCodexAccounts", () => {
	it("preserves unrequested accounts for targeted ensures and derives active-first stable order", () => {
		const current = response([
			account("a", "2026-01-02T00:00:00Z", true),
			account("b", "2026-01-01T00:00:00Z"),
			account("c", "2026-01-01T00:00:00Z"),
		], "a");
		const incoming = response([account("b", "2026-01-03T00:00:00Z", false)], "b");

		const merged = mergeCodexAccounts(current, incoming, "preserveMissing");

		expect(merged.accounts.map(({ id, active }) => [id, active])).toEqual([
			["b", true],
			["c", false],
			["a", false],
		]);
		expect(merged.accountRevision).toBe(7);
	});

	it("removes absent accounts for authoritative GET, mutation, and SSE snapshots", () => {
		const current = response([account("a", "2026-01-01T00:00:00Z"), account("b", "2026-01-02T00:00:00Z")], "a");
		const incoming = response([account("b", "2026-01-02T00:00:00Z", false)], "b");

		expect(mergeCodexAccounts(current, incoming, "replace").accounts).toEqual([
			expect.objectContaining({ id: "b", active: true }),
		]);
	});
});

describe("codexAuthenticationDisplay", () => {
	const display = (state: string, freshness: string, reasonCode: string, status = "valid") => codexAuthenticationDisplay({
		status,
		authentication: { state, freshness, reasonCode },
	} as Parameters<typeof codexAuthenticationDisplay>[0]);

	it("turns inconclusive authentication into an actionable retry", () => {
		expect(display("unknown", "stale", "auth_check_failed")).toEqual({
			key: "settings.codexAccounts.authenticationCheckFailed",
			action: "retry",
			checking: false,
		});
		expect(display("unknown", "stale", "auth_check_timeout").key).toBe("settings.codexAccounts.authenticationCheckTimeout");
	});

	it("keeps checking, unsupported, and rejected credentials distinct", () => {
		expect(display("unknown", "checking", "checking").key).toBe("settings.codexAccounts.authenticationChecking");
		expect(display("unknown", "stale", "auth_check_unsupported").key).toBe("settings.codexAccounts.authenticationUpdateRequired");
		expect(display("unauthorized", "fresh", "unauthorized")).toEqual({
			key: "settings.codexAccounts.reason.authUnauthorized",
			action: "reauthenticate",
			checking: false,
		});
	});
});

it("keeps account mutations fenced while recovery is required", () => {
	const display = codexSwitchDisplay({
		id: "switch-1",
		sourceKind: "managed",
		sourceAccountId: "account-a",
		targetAccountId: "account-b",
		restartRunningSessions: true,
		phase: "recovery_required",
		canRecover: true,
		sessions: [],
		createdAt: "2026-09-02T00:00:00Z",
		updatedAt: "2026-09-02T00:01:00Z",
	} satisfies CodexAccountSwitch);

	expect(display.busy).toBe(false);
	expect(display.mutationBlocked).toBe(true);
	expect(display.canRecover).toBe(true);
	expect(display.recoveryKind).toBe("account");
});

	it("shows active rollback as progress and exposes interrupted rollback recovery", () => {
		const active = codexSwitchDisplay({
			id: "switch-1", sourceKind: "managed", sourceAccountId: "account-a", targetAccountId: "account-b",
			restartRunningSessions: true,
		phase: "rollback_required", failureCode: "activation_unconfirmed", canRecover: false,
		sessions: [], createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
	} satisfies CodexAccountSwitch);
	expect(active.key).toBe("settings.codexAccounts.switch.rollback_required");
	expect(active.busy).toBe(true);
	expect(active.mutationBlocked).toBe(true);
	expect(active.canRecover).toBe(false);

		const interrupted = codexSwitchDisplay({
			id: "switch-1", sourceKind: "managed", sourceAccountId: "account-a", targetAccountId: "account-b",
			restartRunningSessions: true,
		phase: "rollback_required", failureCode: "activation_unconfirmed", canRecover: true,
		sessions: [], createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
	} satisfies CodexAccountSwitch);
	expect(interrupted.busy).toBe(false);
	expect(interrupted.mutationBlocked).toBe(true);
	expect(interrupted.canRecover).toBe(true);
});

it("presents every normal phase as the same switch progress with either session policy", () => {
	for (const restartRunningSessions of [false, true]) {
		for (const phase of ["requested", "stopping_sessions", "sessions_stopped", "checkpointing_source", "activating_target", "verifying_target", "restarting_sessions"] as const) {
			const display = codexSwitchDisplay({
				id: "switch-in-progress", sourceAccountId: "account-a", targetAccountId: "account-b",
				restartRunningSessions, phase, canRecover: false, sessions: [],
				createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
			} satisfies CodexAccountSwitch);
			expect(display.key).toBe("settings.codexAccounts.switch.requested");
		}
	}
});

it("shows a simple reconnect recovery only when restarted sessions need attention", () => {
	const sessions = codexSwitchDisplay({
		id: "switch-sessions", sourceAccountId: "account-a", targetAccountId: "account-b",
		restartRunningSessions: true, phase: "recovery_required", failureCode: "restart_unconfirmed", canRecover: true,
		sessions: [], createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
	} satisfies CodexAccountSwitch);
	const account = codexSwitchDisplay({
		id: "switch-account", sourceAccountId: "account-a", targetAccountId: "account-b",
		restartRunningSessions: true, phase: "recovery_required", failureCode: "activation_unconfirmed", canRecover: true,
		sessions: [], createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
	} satisfies CodexAccountSwitch);

	expect(sessions.key).toBe("settings.codexAccounts.switch.sessions_recovery_required");
	expect(sessions.recoveryKind).toBe("sessions");
	expect(account.key).toBe("settings.codexAccounts.switch.recovery_required");
	expect(account.recoveryKind).toBe("account");
});

it("maps every account reason to complete native locale copy with a safe unknown fallback", () => {
	const locales: AppLocale[] = ["en", "de", "es", "fr", "ja", "ko", "pt-BR", "zh-CN"];
	const switchKeys = ["requested", "stopping_sessions", "sessions_stopped", "checkpointing_source", "activating_target", "verifying_target", "restarting_sessions", "rollback_required", "recovery_required", "sessions_recovery_required", "completed", "failed", "unknown"].map((phase) => `settings.codexAccounts.switch.${phase}`);
	const keys = [
		...codexAccountReasonCodes.map(codexAccountReasonKey),
		...switchKeys,
		"settings.codexAccounts.authenticationChecking",
		"settings.codexAccounts.authenticationCheckFailed",
		"settings.codexAccounts.authenticationCheckTimeout",
		"settings.codexAccounts.authenticationUpdateRequired",
		"settings.codexAccounts.authenticationCodexNotInstalled",
		"settings.codexAccounts.authenticationRetryFailed",
		"settings.codexAccounts.retryingAuthentication",
		"settings.codexAccounts.tryAgain",
		"settings.codexAccounts.noActiveAccount",
		"settings.codexAccounts.deviceRefreshFailed",
		"settings.codexAccounts.switch.restored",
		"settings.codexAccounts.retryRecovery",
		"settings.codexAccounts.reconnectSessions",
		"settings.codexAccounts.switchAndRestart",
		"settings.codexAccounts.restartRunningSessions",
		"settings.codexAccounts.restartRunningSessionsInfo",
		"settings.codexAccounts.restartRunningSessionsTooltip",
		"settings.codexAccounts.restartRunningSessionsOn",
		"settings.codexAccounts.restartRunningSessionsOff",
		"settings.codexAccounts.externalSessionsWarning",
	];
	for (const locale of locales) {
		const catalog = catalogFor(locale);
		for (const key of keys) {
			const value = catalog[key as keyof typeof catalog];
			expect(value, `${locale}: ${key}`).toBeTruthy();
			expect(value).not.toBe(key);
		}
	}
	expect(codexAccountReasonKey("provider-private-message")).toBe("settings.codexAccounts.reason.unknown");
});
