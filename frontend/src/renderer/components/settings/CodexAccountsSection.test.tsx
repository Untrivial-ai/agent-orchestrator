import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { writeCodexAccounts } from "../../hooks/codex-accounts-state";
import { useUiStore } from "../../stores/ui-store";
import { CodexAccountsSection } from "./CodexAccountsSection";

const { deleteMock, getMock, postMock, scrollIntoViewMock, terminalStateCallback, terminalTarget } = vi.hoisted(() => ({
	deleteMock: vi.fn(),
	getMock: vi.fn(),
	postMock: vi.fn(),
	scrollIntoViewMock: vi.fn(),
	terminalStateCallback: { value: undefined as ((state: "exited" | "error") => void) | undefined },
	terminalTarget: { value: undefined as { handleId: string; generation: string; title: string } | undefined },
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { DELETE: deleteMock, GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));

vi.mock("../TerminalPane", () => ({
	TerminalPane: ({ onTerminalStateChange, terminalTarget: target }: { onTerminalStateChange?: (state: "exited" | "error") => void; terminalTarget: { handleId: string; generation: string; title: string } }) => {
		terminalStateCallback.value = onTerminalStateChange;
		terminalTarget.value = target;
		return <div data-testid="inline-terminal-body" />;
	},
}));

const capability = (state = "supported") => ({ state, reasonCode: state, reason: state === "supported" ? "Available." : "Unavailable." });
const authentication = { state: "authorized", freshness: "fresh", checkedAt: "2026-08-31T10:00:00Z", attemptedAt: "2026-08-31T10:00:00Z", reasonCode: "authorized", reason: "Codex is signed in." };
const capacity = { state: "available", freshness: "fresh", plan: "pro", usedPercent: 4, remainingPercent: 96, resetsAt: null, observedAt: "2026-08-31T10:00:00Z", checkedAt: "2026-08-31T10:00:00Z", attemptedAt: "2026-08-31T10:00:00Z", reasonCode: "capacity_available", reason: "Capacity is available.", overall: null, additionalBuckets: [] };
const activeAccount = { id: "11111111-1111-4111-8111-111111111111", label: "active@example.com", source: "managed", status: "valid", reasonCode: "account_valid", reason: "Available.", active: true, authentication, authMethod: "chatgpt", accountEmail: "active@example.com", capacity, createdAt: "2026-08-31T09:00:00Z" };
const inactiveAccount = { ...activeAccount, id: "22222222-2222-4222-8222-222222222222", label: "other@example.com", accountEmail: "other@example.com", active: false, createdAt: "2026-08-31T09:05:00Z" };
const accountResponse = {
	activeAccountId: activeAccount.id,
	accountRevision: 3,
	accounts: [activeAccount, inactiveAccount],
	capabilities: {
		accountRead: capability(), nativeLogin: capability(), capacityRead: capability(), usageRead: capability("unsupported"),
		resetCreditConsume: capability(), threadResume: capability(), accountManagement: capability(), globalSwitch: capability(),
	},
};
const pendingLogin = {
	operation: { operationId: "login-1", status: "pending", reasonCode: "login_pending", reason: "Waiting for Codex sign-in.", expiresAt: "2026-08-31T10:15:00Z" },
	shellTerminal: { handleId: "shellterm-login-1", title: "Add Codex account", createdAt: "2026-08-31T10:00:00Z" },
};

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return { queryClient, ...render(<QueryClientProvider client={queryClient}><CodexAccountsSection /></QueryClientProvider>) };
}

beforeEach(() => {
	Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoViewMock });
	scrollIntoViewMock.mockReset();
	terminalStateCallback.value = undefined;
	terminalTarget.value = undefined;
	useUiStore.setState({ settingsModal: { scope: "global", section: "agents" } });
	getMock.mockReset().mockResolvedValue({ data: accountResponse });
	deleteMock.mockReset().mockResolvedValue({ data: accountResponse });
	postMock.mockReset().mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/codex/accounts/login-terminal") return Promise.resolve({ data: pendingLogin });
		return Promise.resolve({ data: {} });
	});
});

it("shows active-first account cards with correct remaining capacity", async () => {
	renderSection();
	expect(await screen.findByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("In use")).toBeInTheDocument();
	expect(screen.getAllByText(/96% remaining/).length).toBeGreaterThan(0);
	expect(screen.queryByText(/The selected account is the device/)).not.toBeInTheDocument();
	expect(screen.queryByText(/credential/i)).not.toBeInTheDocument();
	expect(screen.queryByText(/billing/i)).not.toBeInTheDocument();
});

it("preserves the complete account list when expanding accounts performs targeted ensures", async () => {
	postMock.mockImplementation((path: string, request?: { body?: { accountIds?: string[] } }) => {
		if (path !== "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: {} });
		const ids = request?.body?.accountIds ?? [];
		if (ids.length === 0) return Promise.resolve({ data: accountResponse });
		return Promise.resolve({ data: { ...accountResponse, accounts: accountResponse.accounts.filter((account) => ids.includes(account.id)) } });
	});
	const { container } = renderSection();
	expect(await screen.findByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();

	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/codex/accounts/ensure",
		{ body: { accountIds: [activeAccount.id], includeUsage: true } },
	));
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();

	fireEvent.click(container.querySelector(`[data-account-id="${inactiveAccount.id}"] button`) as HTMLButtonElement);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/codex/accounts/ensure",
		{ body: { accountIds: [inactiveAccount.id], includeUsage: true } },
	));
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();
});

it("does not offer switching when the device account has no reconciled source", async () => {
	const unreconciledAccount = { ...activeAccount, active: false };
	const unreconciledResponse = {
		...accountResponse,
		activeAccountId: undefined,
		accounts: [unreconciledAccount],
		unmanagedGlobalAccount: {
			label: activeAccount.label,
			authMethod: activeAccount.authMethod,
			accountEmail: activeAccount.accountEmail,
			reasonCode: "global_credential_store_unsupported",
			reason: "This Codex account is active on the device, but its credential store cannot be switched safely.",
		},
	};
	getMock.mockResolvedValue({ data: unreconciledResponse });
	postMock.mockResolvedValue({ data: unreconciledResponse });

	renderSection();
	expect(await screen.findByText("This device account cannot be switched safely.")).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Switch to this account" })).not.toBeInTheDocument();
});

it("shows recovery as an action instead of indefinite switch progress", async () => {
	const recoveryResponse = {
		...accountResponse,
		currentSwitch: {
			id: "33333333-3333-4333-8333-333333333333",
			phase: "recovery_required",
			failureCode: "restart_unconfirmed",
			canRecover: true,
			sessions: [],
		},
	};
	getMock.mockResolvedValue({ data: recoveryResponse });
	postMock.mockResolvedValue({ data: recoveryResponse });

	renderSection();
	expect(await screen.findByRole("button", { name: "Retry recovery" })).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();
	expect(screen.getByRole("button", { name: "Switch account" })).toBeDisabled();
	expect(screen.getAllByText("AO could not confirm that every session restarted.").length).toBeGreaterThan(0);
	expect(screen.queryByText("restart_unconfirmed")).not.toBeInTheDocument();
});

it("keeps a visible live success outcome when an observed switch disappears on its target", async () => {
	const switchingResponse = {
		...accountResponse,
		currentSwitch: {
			id: "33333333-3333-4333-8333-333333333333",
			phase: "verifying_target",
			failureCode: undefined,
			canRecover: false,
			sourceAccountId: activeAccount.id,
			targetAccountId: inactiveAccount.id,
			createdAt: "2026-08-31T10:00:00Z",
			updatedAt: "2026-08-31T10:01:00Z",
			sessions: [],
		},
	};
	getMock.mockResolvedValue({ data: switchingResponse });
	postMock.mockResolvedValue({ data: switchingResponse });
	const { queryClient } = renderSection();
	await screen.findByLabelText("Verifying the selected account…");

	act(() => queryClient.setQueryData(["codex-accounts"], {
		...accountResponse,
		accountRevision: 4,
		activeAccountId: inactiveAccount.id,
		accounts: [{ ...inactiveAccount, active: true }, { ...activeAccount, active: false }],
	}));

	const outcome = await screen.findByRole("status");
	expect(outcome).toHaveTextContent("The device Codex account was switched.");
	expect(outcome).toHaveAttribute("aria-live", "polite");
	expect(outcome).toBeVisible();
});

it("reports when a failed switch safely restores the previous account", async () => {
	const switchingResponse = {
		...accountResponse,
		currentSwitch: {
			id: "33333333-3333-4333-8333-333333333333",
			phase: "activating_target",
			failureCode: "activation_unconfirmed",
			canRecover: false,
			sourceAccountId: activeAccount.id,
			targetAccountId: inactiveAccount.id,
			createdAt: "2026-08-31T10:00:00Z",
			updatedAt: "2026-08-31T10:01:00Z",
			sessions: [],
		},
	};
	getMock.mockResolvedValue({ data: switchingResponse });
	postMock.mockResolvedValue({ data: switchingResponse });
	const { queryClient } = renderSection();
	await screen.findByLabelText("Activating the selected account…");

	act(() => queryClient.setQueryData(["codex-accounts"], {
		...accountResponse,
		accountRevision: 4,
		currentSwitch: undefined,
	}));

	const outcome = await screen.findByRole("status");
	expect(outcome).toHaveTextContent("Account switch failed. Your previous Codex account was restored.");
	expect(outcome).toHaveAttribute("aria-live", "polite");
	expect(outcome).toBeVisible();
	expect(screen.queryByText("activation_unconfirmed")).not.toBeInTheDocument();
});

it("presents plan, general and model usage limits with remaining-capacity meters", async () => {
	const detailedAccount = {
		...activeAccount,
		capacity: {
			...capacity,
			usedPercent: 19,
			remainingPercent: 81,
			resetsAt: "2026-09-07T02:32:14Z",
			overall: {
				limitId: "codex",
				reached: "not_reached",
				primary: { usedPercent: 19, windowDurationMinutes: 10080, resetsAt: "2026-09-07T02:32:14Z" },
			},
			additionalBuckets: [{
				limitId: "spark-internal",
				displayName: "GPT-5.3-Codex-Spark",
				reached: "not_reached",
				primary: { usedPercent: 0, windowDurationMinutes: 300, resetsAt: "2026-08-31T21:09:40Z" },
				secondary: { usedPercent: 0, windowDurationMinutes: 10080, resetsAt: "2026-09-07T16:32:40Z" },
			}],
		},
		usageSummary: {
			latestDayTokens: 34904480,
			latestDayStartDate: "2026-08-31",
			lifetimeTokens: 54571452296,
			peakDailyTokens: 2000000000,
			longestRunningTurnSeconds: 26340,
			currentStreakDays: 2,
			longestStreakDays: 99,
			observedAt: "2026-08-31T10:00:00Z",
		},
	};
	const detailedResponse = { ...accountResponse, accounts: [detailedAccount, inactiveAccount] };
	getMock.mockResolvedValue({ data: detailedResponse });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/codex/accounts/ensure"
		? Promise.resolve({ data: detailedResponse })
		: Promise.resolve({ data: {} }));
	const { container } = renderSection();
	await screen.findAllByText(/81% remaining/);
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);

	expect(await screen.findByRole("region", { name: "Your plan" })).toBeInTheDocument();
	expect(screen.getByRole("region", { name: "Activity" })).toBeInTheDocument();
	expect(screen.getByText("Pro plan")).toBeInTheDocument();
	expect(screen.getByText("General usage limits")).toBeInTheDocument();
	expect(screen.getAllByText("Weekly usage limit")).toHaveLength(2);
	expect(screen.getByText("GPT-5.3-Codex-Spark usage limits")).toBeInTheDocument();
	expect(screen.getByText("5-hour usage limit")).toBeInTheDocument();
	const weeklyMeter = screen.getByRole("progressbar", { name: /Weekly usage limit, 81% left/ });
	expect(weeklyMeter).toHaveAttribute("aria-valuenow", "81");
	expect(screen.queryByText("34.9M tokens")).not.toBeInTheDocument();
	expect(screen.getByText("54.6B tokens")).toBeInTheDocument();
	expect(screen.getByText("2B tokens")).toBeInTheDocument();
	expect(screen.getByText("7h 19m")).toBeInTheDocument();
	expect(screen.getByText("2 days")).toBeInTheDocument();
	expect(screen.getByText("99 days")).toBeInTheDocument();
	const activityMetrics = screen.getByTestId("codex-account-activity-metrics");
	expect(activityMetrics.children).toHaveLength(5);
	expect(activityMetrics).toHaveStyle({ gridTemplateColumns: "repeat(5, minmax(0, 1fr))" });
	expect(activityMetrics.parentElement).not.toHaveClass("overflow-x-auto");
	expect(screen.queryByText("19% used")).not.toBeInTheDocument();
	expect(screen.queryByText("54571452296")).not.toBeInTheDocument();
});

it("shows provider-reported resets and confirms before consuming one", async () => {
	const accountWithReset = {
		...activeAccount,
		capacity: {
			...capacity,
			resetCredits: { availableCount: 1, nearestExpiresAt: "2026-09-21T00:15:00Z" },
		},
	};
	const responseWithReset = { ...accountResponse, accounts: [accountWithReset, inactiveAccount] };
	const responseAfterReset = {
		...responseWithReset,
		accounts: [{ ...accountWithReset, capacity: { ...accountWithReset.capacity, resetCredits: { availableCount: 0 } } }, inactiveAccount],
	};
	vi.stubGlobal("crypto", { randomUUID: () => "reset-request-1" });
	getMock.mockResolvedValue({ data: responseWithReset });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: responseWithReset });
		if (path === "/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume") return Promise.resolve({ data: responseAfterReset });
		return Promise.resolve({ data: {} });
	});
	const { container } = renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	expect(await screen.findByText("1 reset available")).toBeInTheDocument();
	fireEvent.click(screen.getByRole("button", { name: "Use reset" }));
	expect(await screen.findByText("Use a usage-limit reset?")).toBeInTheDocument();
	const resetButtons = screen.getAllByRole("button", { name: "Use reset" });
	fireEvent.click(resetButtons[resetButtons.length - 1]);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume",
		{ params: { path: { accountId: activeAccount.id } }, body: { idempotencyKey: "reset-request-1" } },
	));
	await waitFor(() => expect(screen.getByText("No resets available")).toBeInTheDocument());
	vi.unstubAllGlobals();
});

it("reuses a reset idempotency key after a failed confirmed retry", async () => {
	const accountWithReset = { ...activeAccount, capacity: { ...capacity, resetCredits: { availableCount: 1 } } };
	const response = { ...accountResponse, accounts: [accountWithReset, inactiveAccount] };
	vi.stubGlobal("crypto", { randomUUID: () => "stable-reset-key" });
	getMock.mockResolvedValue({ data: response });
	let attempts = 0;
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: response });
		if (path === "/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume") {
			attempts += 1;
			return attempts === 1 ? Promise.resolve({ error: new Error("temporary failure") }) : Promise.resolve({ data: response });
		}
		return Promise.resolve({ data: {} });
	});
	const { container } = renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	fireEvent.click(await screen.findByRole("button", { name: "Use reset" }));
	let dialog = await screen.findByRole("dialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Use reset" }));
	await screen.findAllByText("temporary failure");
	dialog = screen.getByRole("dialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Use reset" }));
	await waitFor(() => expect(postMock.mock.calls.filter(([path]) => path === "/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume")).toHaveLength(2));
	const resetCalls = postMock.mock.calls.filter(([path]) => path === "/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume");
	expect(resetCalls.map(([, request]) => request.body.idempotencyKey)).toEqual(["stable-reset-key", "stable-reset-key"]);
	vi.unstubAllGlobals();
});

it("uses safe fallback headings and preserves stale values without exposing raw limit ids", async () => {
	const staleAccount = {
		...activeAccount,
		capacity: {
			...capacity,
			freshness: "stale",
			checkedAt: "2026-08-31T10:00:00Z",
			overall: null,
			additionalBuckets: [{
				limitId: "provider-secret-bucket-id",
				reached: "not_reached",
				primary: { usedPercent: 75, windowDurationMinutes: 60, resetsAt: null },
			}],
		},
	};
	const staleResponse = { ...accountResponse, accounts: [staleAccount] };
	getMock.mockResolvedValue({ data: staleResponse });
	postMock.mockResolvedValue({ data: staleResponse });
	const { container } = renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);

	expect(await screen.findByText("Additional usage limits")).toBeInTheDocument();
	expect(screen.queryByText("provider-secret-bucket-id")).not.toBeInTheDocument();
	expect(screen.getByRole("status")).toHaveTextContent(/Usage information may be out of date/);
	expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "25");
});

it("collapses the provider while rotating only its chevron", async () => {
	renderSection();
	await screen.findByText("active@example.com");
	const providerToggle = screen.getByRole("button", { name: /Codex/ });
	const icon = providerToggle.querySelector("img");
	const chevron = providerToggle.querySelector("svg");
	expect(icon).not.toBeNull();
	expect(chevron).not.toBeNull();
	fireEvent.click(providerToggle);
	expect(screen.queryByText("active@example.com")).not.toBeInTheDocument();
	expect(icon?.getAttribute("class")).not.toContain("rotate");
	expect(chevron?.getAttribute("class")).toContain("rotate");
});

it("starts account login immediately with no name prompt and auto-scrolls the inline terminal", async () => {
	renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(screen.getByRole("button", { name: "Add account" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/codex/accounts/login-terminal"));
	expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
	expect(await screen.findByTestId("inline-terminal-body")).toBeInTheDocument();
	expect(scrollIntoViewMock).toHaveBeenCalledWith({ behavior: "smooth", block: "nearest" });
	expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "agents" });
	expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();
});

it("reattaches a daemon-projected login terminal across remounts without opening or cancelling it", async () => {
	const activeLogin = {
		operationId: "login-rehydrate",
		status: "pending",
		reasonCode: "login_pending",
		reason: "private daemon copy",
		expiresAt: "2026-08-31T10:15:00Z",
		shellTerminal: { handleId: "shellterm-rehydrate", title: "Existing sign-in", createdAt: "2026-08-31T10:00:00Z" },
	};
	const projected = { ...accountResponse, activeLogin };
	getMock.mockResolvedValue({ data: projected });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/codex/accounts/ensure" ? Promise.resolve({ data: projected }) : Promise.resolve({ data: {} }));
	const first = renderSection();
	await screen.findByTestId("inline-terminal-body");
	expect(terminalTarget.value).toMatchObject({ handleId: "shellterm-rehydrate", generation: "2026-08-31T10:00:00Z", title: "Existing sign-in" });
	expect(postMock.mock.calls.filter(([path]) => String(path).includes("login-terminal") || String(path).includes("/cancel"))).toHaveLength(0);
	first.unmount();
	renderSection();
	await screen.findByTestId("inline-terminal-body");
	expect(terminalTarget.value?.handleId).toBe("shellterm-rehydrate");
	expect(postMock.mock.calls.filter(([path]) => String(path).includes("login-terminal") || String(path).includes("/cancel"))).toHaveLength(0);
});

it("surfaces account-service unavailability instead of loading forever", async () => {
	const unavailable = new Error("Codex account management is unavailable");
	getMock.mockResolvedValue({ error: unavailable });
	postMock.mockResolvedValue({ error: unavailable });
	renderSection();

	expect((await screen.findAllByText("Codex account management is unavailable", {}, { timeout: 3_000 })).length).toBeGreaterThan(0);
	expect(screen.queryByText("Loading Codex accounts…")).not.toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();
});

it("verifies exactly once on terminal exit and collapses after structured success", async () => {
	const completedAccount = { ...inactiveAccount, id: "33333333-3333-4333-8333-333333333333", label: "new@example.com", accountEmail: "new@example.com" };
	// The verified operation is enough to update the card immediately. A
	// follow-up cached-list refresh is best-effort and must not keep the dead
	// terminal open when it fails.
	getMock.mockResolvedValueOnce({ data: accountResponse }).mockRejectedValue(new Error("refresh unavailable"));
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/codex/accounts/login-terminal") return Promise.resolve({ data: pendingLogin });
		if (path.includes("/verify")) return Promise.resolve({ data: { ...pendingLogin.operation, status: "completed", reasonCode: "login_completed", reason: "Codex account added.", account: completedAccount } });
		return Promise.resolve({ data: {} });
	});
	renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(screen.getByRole("button", { name: "Add account" }));
	await screen.findByTestId("inline-terminal-body");
	act(() => terminalStateCallback.value?.("exited"));
	await waitFor(() => expect(postMock.mock.calls.filter(([path]) => String(path).includes("/verify"))).toHaveLength(1));
	await waitFor(() => expect(screen.queryByTestId("inline-terminal-body")).not.toBeInTheDocument());
	expect(await screen.findByText("new@example.com")).toBeInTheDocument();
});

it("retains terminal output when verification is unauthorized", async () => {
	const unauthorizedLogin = {
		...pendingLogin,
		operation: { ...pendingLogin.operation, operationId: "login-unauthorized" },
		shellTerminal: { ...pendingLogin.shellTerminal, handleId: "shellterm-login-unauthorized" },
	};
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/codex/accounts/login-terminal") return Promise.resolve({ data: unauthorizedLogin });
		if (path.includes("/verify")) return Promise.resolve({ data: { ...unauthorizedLogin.operation, status: "unauthorized", reasonCode: "login_unauthorized", reason: "Codex is still signed out." } });
		return Promise.resolve({ data: {} });
	});
	renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(screen.getByRole("button", { name: "Add account" }));
	await screen.findByTestId("inline-terminal-body");
	act(() => terminalStateCallback.value?.("exited"));
	expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled();
	expect(screen.getByTestId("inline-terminal-body")).toBeInTheDocument();
});

it("signs in again inline and replaces the existing account card", async () => {
	const signedOutAccount = {
		...inactiveAccount,
		status: "signed_out",
		reasonCode: "account_signed_out",
		reason: "This Codex account is signed out.",
		authentication: { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Sign in again to use this Codex account." },
	};
	const signedOutResponse = { ...accountResponse, accounts: [activeAccount, signedOutAccount] };
	const restoredAccount = { ...signedOutAccount, status: "valid", reasonCode: "account_valid", reason: "Available.", authentication };
	const restoredResponse = { ...accountResponse, accounts: [activeAccount, restoredAccount] };
	const reauthentication = {
		...pendingLogin,
		operation: { ...pendingLogin.operation, operationId: "reauth-1", accountId: signedOutAccount.id },
		shellTerminal: { ...pendingLogin.shellTerminal, handleId: "shellterm-reauth-1", title: "Sign in to Codex account" },
	};
	getMock.mockResolvedValueOnce({ data: signedOutResponse }).mockResolvedValue({ data: restoredResponse });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: signedOutResponse });
		if (path === "/api/v1/agents/codex/accounts/{accountId}/login-terminal") return Promise.resolve({ data: reauthentication });
		if (path.includes("/verify")) return Promise.resolve({ data: { ...reauthentication.operation, status: "completed", reasonCode: "login_completed", reason: "Codex account signed in.", account: restoredAccount } });
		return Promise.resolve({ data: {} });
	});
	const { container } = renderSection();
	await screen.findByText("other@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${signedOutAccount.id}"] button`) as HTMLButtonElement);
	expect(await screen.findByRole("button", { name: "Sign in again" })).toBeEnabled();
	fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/codex/accounts/{accountId}/login-terminal",
		{ params: { path: { accountId: signedOutAccount.id } } },
	));
	expect(await screen.findByTestId("inline-terminal-body")).toBeInTheDocument();
	expect(screen.getByTestId("codex-account-login-terminal")).toBeInTheDocument();
	act(() => terminalStateCallback.value?.("exited"));
	await waitFor(() => expect(screen.queryByTestId("inline-terminal-body")).not.toBeInTheDocument());
	expect(container.querySelectorAll(`[data-account-id="${signedOutAccount.id}"]`)).toHaveLength(1);
	expect(within(container.querySelector(`[data-account-id="${signedOutAccount.id}"]`) as HTMLElement).getByText("Signed in")).toBeInTheDocument();
});

it("logs out a signed-in account while retaining its card", async () => {
	const signedOutAccount = {
		...activeAccount,
		active: false,
		status: "signed_out",
		reasonCode: "account_signed_out",
		reason: "This Codex account is signed out.",
		authentication: { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Sign in again to use this Codex account." },
		capacity: { ...capacity, state: "unknown", remainingPercent: null, usedPercent: null },
	};
	const signedOutResponse = { ...accountResponse, activeAccountId: undefined, accountRevision: 4, accounts: [signedOutAccount, inactiveAccount] };
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/codex/accounts/{accountId}/logout") return Promise.resolve({ data: signedOutResponse });
		return Promise.resolve({ data: {} });
	});
	const { container } = renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	fireEvent.click(await screen.findByRole("button", { name: "Log out" }));
	const dialog = await screen.findByRole("dialog");
	expect(dialog).toHaveTextContent("Log out of this Codex account?");
	fireEvent.click(within(dialog).getByRole("button", { name: "Log out" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/codex/accounts/{accountId}/logout",
		{ params: { path: { accountId: activeAccount.id } } },
	));
	const row = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	await waitFor(() => expect(within(row).getByText("Signed out")).toBeInTheDocument());
	expect(within(row).getByRole("button", { name: "Sign in again" })).toBeEnabled();
});

it("deletes a signed-out account after confirmation", async () => {
	const signedOutAccount = {
		...inactiveAccount,
		status: "signed_out",
		reasonCode: "account_signed_out",
		reason: "This Codex account is signed out.",
		authentication: { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Sign in again to use this Codex account." },
	};
	const signedOutResponse = { ...accountResponse, accounts: [activeAccount, signedOutAccount] };
	const deletedResponse = { ...accountResponse, accounts: [activeAccount] };
	getMock.mockResolvedValue({ data: signedOutResponse });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: signedOutResponse });
		return Promise.resolve({ data: {} });
	});
	deleteMock.mockResolvedValue({ data: deletedResponse });

	const { container } = renderSection();
	await screen.findByText("other@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${signedOutAccount.id}"] button`) as HTMLButtonElement);
	expect(await screen.findByRole("button", { name: "Delete account" })).toBeEnabled();
	fireEvent.click(screen.getByRole("button", { name: "Delete account" }));
	const dialog = await screen.findByRole("dialog");
	expect(dialog).toHaveTextContent("Delete this Codex account?");
	fireEvent.click(within(dialog).getByRole("button", { name: "Delete account" }));

	await waitFor(() => expect(deleteMock).toHaveBeenCalledWith(
		"/api/v1/agents/codex/accounts/{accountId}",
		{ params: { path: { accountId: signedOutAccount.id } } },
	));
	await waitFor(() => expect(screen.queryByText("other@example.com")).not.toBeInTheDocument());
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
});

it("starts a global switch with the displayed account revision", async () => {
	const switchOperation = { id: "switch-1", phase: "requested", failureCode: null, canRecover: false };
	vi.stubGlobal("crypto", { randomUUID: () => "idempotency-1" });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/codex/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/codex/account-switches") return Promise.resolve({ data: switchOperation });
		return Promise.resolve({ data: pendingLogin });
	});
	renderSection();
	await screen.findByText("other@example.com");
	expect(screen.queryByRole("button", { name: "Switch to this account" })).not.toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Switch account" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: /other@example.com/ }));
	const dialog = await screen.findByRole("dialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Switch account" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/codex/account-switches", {
		body: { targetAccountId: inactiveAccount.id, expectedAccountRevision: 3, idempotencyKey: "idempotency-1" },
	}));
	vi.unstubAllGlobals();
});

const unauthorizedAuthentication = { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Codex needs authentication." };
const launchFailureResponse = {
	...accountResponse,
	accounts: [{ ...activeAccount, authentication: unauthorizedAuthentication }, inactiveAccount],
};

it("shows the active account's reauthentication state and CTA as soon as a failed launch publishes it", async () => {
	const { container, queryClient } = renderSection();
	expect(await screen.findByText("active@example.com · Pro · 96% remaining")).toBeInTheDocument();
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	expect(await screen.findByRole("button", { name: "Log out" })).toBeInTheDocument();
	const reads = getMock.mock.calls.length;

	// The daemon publishes the account event the rejected spawn produced.
	act(() => { writeCodexAccounts(queryClient, launchFailureResponse as never, "replace"); });

	expect(await screen.findByRole("button", { name: "Sign in again" })).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Log out" })).not.toBeInTheDocument();
	expect(screen.getByText("active@example.com · Signed out")).toBeInTheDocument();
	expect(getMock.mock.calls.length).toBe(reads);
});

it("does not let an authorized inactive account mask the active account's reauthentication", async () => {
	getMock.mockResolvedValue({ data: launchFailureResponse });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/codex/accounts/ensure" ? Promise.resolve({ data: launchFailureResponse }) : Promise.resolve({ data: {} }));
	const { container } = renderSection();

	expect(await screen.findByText("active@example.com · Signed out")).toBeInTheDocument();
	const activeRow = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	const inactiveRow = container.querySelector(`[data-account-id="${inactiveAccount.id}"]`) as HTMLElement;
	expect(within(activeRow).getByText("Signed out")).toBeInTheDocument();
	expect(within(inactiveRow).getByText("Signed in")).toBeInTheDocument();
	expect(within(activeRow).queryByText("Signed in")).not.toBeInTheDocument();
});

it("restores the signed-in state after a successful reauthentication", async () => {
	getMock.mockResolvedValue({ data: launchFailureResponse });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/codex/accounts/ensure" ? Promise.resolve({ data: launchFailureResponse }) : Promise.resolve({ data: {} }));
	const { container, queryClient } = renderSection();
	expect(await screen.findByText("active@example.com · Signed out")).toBeInTheDocument();
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	expect(await screen.findByRole("button", { name: "Sign in again" })).toBeInTheDocument();

	act(() => { writeCodexAccounts(queryClient, accountResponse as never, "replace"); });

	expect(await screen.findByRole("button", { name: "Log out" })).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Sign in again" })).not.toBeInTheDocument();
	expect(screen.getByText("active@example.com · Pro · 96% remaining")).toBeInTheDocument();
});
