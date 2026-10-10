import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ProviderAccount } from "../hooks/useProviderAccounts";
import { AccountAlertsRuntime } from "./AccountAlertsRuntime";

const api = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: api.get }, apiErrorMessage: (error: { message: string }) => error.message }));

const notice = vi.fn(function Notification() {});
const account = (id: string, displayName: string, extra: Partial<ProviderAccount> = {}): ProviderAccount => ({ id, provider: "codex", displayName, email: `${id}@example.test`, signedIn: true, primary: false, sessions: [], ...extra });
const windows = (weekly: number, resetTime: string) => [{ durationSeconds: 18000, remainingFraction: 0.5 }, { durationSeconds: 604800, remainingFraction: weekly, resetTime }, { scope: "code_review" as const, remainingFraction: 0.01 }];
let accounts: ProviderAccount[];
const usageReads = () => api.get.mock.calls.filter(([, options]) => options.params.query.includeUsage).length;
// Each start reads the accounts afresh, the way the app does after a restart or its next poll, and ends once
// the answer named by the key (with usage, or the catalogue alone) has reached the runtime.
async function watch(key = ["provider-accounts"]) {
	const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(<QueryClientProvider client={cache}><AccountAlertsRuntime /></QueryClientProvider>);
	await waitFor(() => expect(cache.getQueryData(key)).toBeDefined());
	await act(() => new Promise(resolve => setTimeout(resolve, 20)));
	view.unmount();
}
beforeEach(() => {
	vi.clearAllMocks();
	localStorage.clear();
	vi.stubGlobal("Notification", notice);
	api.get.mockImplementation(async () => ({ data: { accounts: structuredClone(accounts) } }));
});
afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
});

it("warns once per limit window when the lowest general limit is at or under the account's warn level", async () => {
	accounts = [account("a", "Cedar Codex", { warnAt: 20, usage: { status: "available", windows: windows(0.2, "2030-01-07T00:00:00Z") } }), account("b", "Maple Codex", { warnAt: 10, usage: { status: "available", windows: windows(0.2, "2030-01-07T00:00:00Z") } })];
	await watch();
	// A limit that covers one thing only does not count, and an account above its own level is left alone.
	expect(notice.mock.calls).toEqual([["Cedar Codex is running low", { body: "20% of its limit is left." }]]);
	accounts[0].usage!.windows![1].remainingFraction = 0.12;
	await watch();
	expect(notice).toHaveBeenCalledTimes(1);
	// The next window is a new thing to say.
	accounts[0].usage!.windows![1] = { durationSeconds: 604800, remainingFraction: 0.05, resetTime: "2030-01-14T00:00:00Z" };
	await watch();
	expect(notice.mock.calls.slice(1)).toEqual([["Cedar Codex is running low", { body: "5% of its limit is left." }]]);
});
it("says once that a reached limit moved an account's sessions on", async () => {
	accounts = [account("a", "Cedar Codex", { onLimit: "b", moved: { at: "2030-01-05T10:00:00Z", sessions: 2, to: "b" } }), account("b", "Maple Codex")];
	await watch();
	await watch();
	expect(notice.mock.calls).toEqual([["Cedar Codex reached its limit", { body: "2 sessions moved to Maple Codex." }]]);
	accounts[0].moved = { at: "2030-01-06T09:00:00Z", sessions: 1, to: "b" };
	await watch();
	expect(notice.mock.calls.slice(1)).toEqual([["Cedar Codex reached its limit", { body: "1 session moved to Maple Codex." }]]);
});
it("reads no usage and says nothing while no account asked to be watched", async () => {
	accounts = [account("a", "Cedar Codex", { usage: { status: "available", windows: windows(0, "2030-01-07T00:00:00Z") } })];
	await watch(["provider-accounts", "catalogue"]);
	expect(api.get).toHaveBeenCalledWith("/api/v1/provider-accounts", { params: { query: { includeUsage: false, refresh: false } } });
	expect(usageReads()).toBe(0);
	expect(notice).not.toHaveBeenCalled();
});
