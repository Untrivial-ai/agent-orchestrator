import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { SessionActionsMenu } from "./SessionActionsMenu";
import { SessionProviderAccountMenuItem } from "./SessionProviderAccountMenuItem";

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: api.get, POST: api.post }, apiErrorMessage: (error: { message: string }) => error.message }));

const account = (id: string, displayName: string, left?: number, extra = {}) => ({ id, provider: "codex", displayName, email: `${id}@example.test`, signedIn: true, primary: false, sessions: [], usage: left === undefined ? undefined : { status: "available", windows: [{ durationSeconds: 18000, remainingFraction: left }] }, ...extra });
const accounts = [account("alice", "Harbor Codex", 0.62, { primary: true }), account("bob", "Summit Codex", 0.14), account("erin", "Pine Codex", 1, { signedIn: false }), account("clara", "Willow Claude", 1, { provider: "claude" })];
let route = { managed: true, accountId: "alice" };
const SESSION = "/api/v1/provider-accounts/sessions/{sessionId}";
// Opens the session menu and the account submenu inside it.
async function openAccounts() {
	const user = userEvent.setup();
	render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><SessionActionsMenu><SessionProviderAccountMenuItem sessionId="session-1" /></SessionActionsMenu></QueryClientProvider>);
	await user.click(await screen.findByRole("button", { name: "Session actions" }));
	const item = await screen.findByRole("menuitem", { name: /^Account/ });
	await user.hover(item);
	await waitFor(() => expect(item).toHaveAttribute("aria-expanded", "true"));
	return item;
}
beforeEach(() => {
	vi.clearAllMocks();
	route = { managed: true, accountId: "alice" };
	api.get.mockImplementation(async (path: string) => path === SESSION ? { data: route } : { data: { accounts } });
	api.post.mockResolvedValue({ data: { accounts } });
});

it("lists the provider's signed-in accounts beside the other session actions and switches the session", async () => {
	// The item names the session's current account before it is opened.
	expect(await openAccounts()).toHaveTextContent("Harbor Codex");
	expect(api.get).toHaveBeenCalledWith(SESSION, { params: { path: { sessionId: "session-1" } } });
	// Each choice shows what is left of its shortest usage window; the current one cannot be chosen again.
	expect((await screen.findAllByRole("menuitem")).map(item => item.textContent).slice(-3)).toEqual(["Harbor CodexDefault62% left", "Summit Codex14% left", "Manage accounts"]);
	expect(screen.getByRole("menuitem", { name: /Harbor Codex.*62% left/ })).toHaveAttribute("aria-disabled", "true");
	fireEvent.click(screen.getByRole("menuitem", { name: /Summit Codex/ }));
	await waitFor(() => expect(api.post).toHaveBeenCalledWith("/api/v1/provider-accounts/{accountId}/actions", { params: { path: { accountId: "bob" } }, body: { action: "assign-session", sessionId: "session-1" } }));
});
it("asks for a sign-in when the session waits for one, and is absent for an unmanaged session", async () => {
	route = { managed: true, accountId: "" };
	await openAccounts();
	expect(await screen.findByRole("menuitem", { name: "No signed-in accounts" })).toBeInTheDocument();
	expect(screen.getByRole("menuitem", { name: "Please sign in again" })).toBeInTheDocument();
	fireEvent.click(screen.getByRole("menuitem", { name: "Manage accounts" }));
	expect(useUiStore.getState().settingsModal).toEqual(expect.objectContaining({ section: "accountManager" }));
	// Outside a menu the item would throw if it rendered, so an unmanaged session renders nothing.
	route = { managed: false, accountId: "" };
	render(<QueryClientProvider client={new QueryClient()}><SessionProviderAccountMenuItem sessionId="session-2" /></QueryClientProvider>);
	await waitFor(() => expect(api.get).toHaveBeenCalledWith(SESSION, { params: { path: { sessionId: "session-2" } } }));
});
