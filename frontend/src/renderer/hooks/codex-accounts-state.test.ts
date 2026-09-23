import { describe, expect, it } from "vitest";
import type { CodexAccountsResponse } from "./useCodexAccountsQuery";
import { catalogFor } from "../i18n/messages";
import type { AppLocale } from "../i18n/locales";
import { codexAccountCanSwitch, codexAccountReasonCodes, codexAccountReasonKey, codexAuthenticationDisplay, codexSwitchDisplay, mergeCodexAccounts } from "./codex-accounts-state";
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

	it("shows no active row until local reconciliation verifies device ownership", () => {
		const current = response([
			account("a", "2026-01-02T00:00:00Z", true),
			account("b", "2026-01-01T00:00:00Z"),
		], "a");
		const incoming = {
			...response([account("a", "2026-01-02T00:00:00Z")], "a"),
			activeAccountId: undefined,
			deviceReconciliation: {
				status: "checking",
				activeAccountVerified: false,
				reasonCode: "checking",
				retryable: false,
			},
		} as CodexAccountsResponse;

		const merged = mergeCodexAccounts(current, incoming, "preserveMissing");

		expect(merged.accounts.map(({ id, active }) => [id, active])).toEqual([
			["b", false],
			["a", false],
		]);
	});

	it("clears the presented active row after reconciliation actually fails", () => {
		const current = response([account("a", "2026-01-01T00:00:00Z", true)], "a");
		const incoming = {
			...response([account("a", "2026-01-01T00:00:00Z")], "a"),
			deviceReconciliation: {
				status: "temporarily_unavailable",
				activeAccountVerified: false,
				reasonCode: "account_read_inconclusive",
				retryable: true,
			},
		} as CodexAccountsResponse;

		expect(mergeCodexAccounts(current, incoming, "preserveMissing").accounts).toEqual([
			expect.objectContaining({ id: "a", active: false }),
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

it("allows a locally valid saved credential to switch regardless of cached authentication", () => {
	expect(codexAccountCanSwitch({ status: "valid" })).toBe(true);
	expect(codexAccountCanSwitch({ status: "signed_out" })).toBe(false);
});

it("automatically settles legacy recovery journals as switch progress", () => {
	const display = codexSwitchDisplay({
		id: "switch-1",
		sourceKind: "managed",
		sourceAccountId: "account-a",
		targetAccountId: "account-b",
		phase: "recovery_required",
		createdAt: "2026-09-02T00:00:00Z",
		updatedAt: "2026-09-02T00:01:00Z",
	} satisfies CodexAccountSwitch);

	expect(display.busy).toBe(true);
	expect(display.mutationBlocked).toBe(true);
	expect(display.key).toBe("settings.codexAccounts.switch.requested");
});

it("presents every normal credential phase as the same switch progress", () => {
	for (const phase of ["requested", "checkpointing_source", "activating_target"] as const) {
		const display = codexSwitchDisplay({
			id: "switch-in-progress", sourceKind: "managed", sourceAccountId: "account-a", targetAccountId: "account-b",
			phase,
			createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
		} satisfies CodexAccountSwitch);
		expect(display.key).toBe("settings.codexAccounts.switch.requested");
	}
});

it("maps every account reason to complete native locale copy with a safe unknown fallback", () => {
	const locales: AppLocale[] = ["en", "de", "es", "fr", "ja", "ko", "pt-BR", "zh-CN"];
	const switchKeys = ["requested", "completed", "failed", "unknown"].map((phase) => `settings.codexAccounts.switch.${phase}`);
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
		"settings.codexAccounts.deviceRefreshFailed",
		"settings.codexAccounts.switch.unchanged",
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
