import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ProviderAccountsSection } from "./ProviderAccountsSection";
import type { AccountAction, ProviderAccount } from "../../hooks/useProviderAccounts";
import { useUiStore } from "../../stores/ui-store";

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), remove: vi.fn(), open: vi.fn(), clipboard: vi.fn(), navigate: vi.fn(), workspaceQuery: vi.fn(), workspaces: [] as unknown[], costs: new Map<string, unknown>() }));
vi.mock("../../lib/api-client", () => ({ apiClient: { GET: mock.get, POST: mock.post, DELETE: mock.remove }, apiErrorMessage: (error: { message: string }) => error.message }));
vi.mock("../../lib/bridge", () => ({ aoBridge: { app: { openExternal: mock.open }, clipboard: { writeText: mock.clipboard } } }));
// What the page reads beside the accounts: the sessions by name, what they cost, and the way to one.
vi.mock("../../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: mock.workspaceQuery }));
vi.mock("../../hooks/useSessionUsageSummaries", () => ({ useSessionUsageSummaries: () => ({ data: mock.costs }) }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mock.navigate }));

const LIST = "/api/v1/provider-accounts";
const ACTIONS = "/api/v1/provider-accounts/{accountId}/actions";
const LOGIN = "/api/v1/provider-accounts/login";
const loginCacheKey = ["provider-account-login"];
const alice: ProviderAccount = { id: "a", provider: "codex", displayName: "Cedar Codex", email: "alice@example.test", signedIn: true, primary: true, sessions: ["session-a"] };
const bob: ProviderAccount = { id: "b", provider: "codex", displayName: "Maple Codex", email: "bob@example.test", signedIn: true, primary: false, sessions: ["session-b", "session-c"] };
const clara: ProviderAccount = { id: "c", provider: "claude", displayName: "Willow Claude", email: "clara@example.test", signedIn: true, primary: true, sessions: [] };
const waitingLogin = (extra: Record<string, unknown> = {}) => ({ id: "login-1", provider: "codex", mode: "browser", url: "https://provider.test/login", status: "waiting", accountId: "", ...extra });

// The daemon double. An action changes the accounts the way the daemon would
// and answers with them; a refusal or a sign-in answer is set per test.
let inventory: { accounts: ProviderAccount[] };
let refusal = "";
let resetOutcome = "reset";
let loginStart: unknown;
let loginStatus: unknown;
function act(accountId: string, body: AccountAction) {
	if (refusal) return { error: { message: refusal } };
	const target = inventory.accounts.find(account => account.id === accountId)!;
	for (const account of inventory.accounts) {
		if (body.action === "primary" && account.provider === target.provider) account.primary = account === target;
		if (body.action === "assign-session") account.sessions = account === target ? [...account.sessions, body.sessionId!] : account.sessions.filter(id => id !== body.sessionId);
	}
	if (body.action === "rename") target.displayName = body.displayName!;
	if (body.action === "settings") Object.assign(target, { reserved: body.reserved ?? target.reserved, onLimit: body.onLimit ?? target.onLimit, warnAt: body.warnAt ?? target.warnAt });
	if (body.action === "sign-out") Object.assign(target, { signedIn: false, sessions: [] });
	if (body.action === "remove") inventory.accounts = inventory.accounts.filter(account => account !== target);
	return { data: { ...structuredClone(inventory), ...(body.action === "reset" ? { resetOutcome } : {}) } };
}
beforeEach(() => {
	vi.clearAllMocks();
	inventory = { accounts: [structuredClone(alice), structuredClone(bob), structuredClone(clara)] };
	refusal = "";
	resetOutcome = "reset";
	loginStart = { data: waitingLogin() };
	loginStatus = { data: waitingLogin() };
	mock.get.mockImplementation(async (path: string) => path === LIST ? { data: structuredClone(inventory) } : loginStatus);
	mock.post.mockImplementation(async (path: string, options: { params: { path: { accountId: string } }; body: AccountAction }) => path === LOGIN ? loginStart : act(options.params.path.accountId, options.body));
	mock.remove.mockResolvedValue({});
	mock.open.mockResolvedValue(undefined);
	mock.clipboard.mockResolvedValue(undefined);
	mock.workspaces = [];
	mock.costs = new Map();
	mock.workspaceQuery.mockImplementation(() => ({ data: mock.workspaces }));
});
afterEach(cleanup);

type User = ReturnType<typeof userEvent.setup>;
function renderAccounts(cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })) {
	const view = render(<QueryClientProvider client={cache}><ProviderAccountsSection /></QueryClientProvider>);
	return { cache, view };
}
const listItem = (account: ProviderAccount) => screen.getByTestId(`provider-account-${account.id}`);
const detail = () => screen.getByTestId("provider-account-detail");
const status = () => screen.findByRole("status");
// One per provider, always the last row of its list.
const addButtons = () => screen.getAllByRole("button", { name: /^New \w+ account/ }) as HTMLButtonElement[];
const actions = (action: string) => mock.post.mock.calls.filter(([path, options]) => path === ACTIONS && options.body.action === action).map(([, options]) => [options.params.path.accountId, options.body]);
async function start() {
	const user = userEvent.setup();
	renderAccounts();
	await screen.findByRole("heading", { name: inventory.accounts[0].displayName });
	return user;
}
async function open(user: User, account: ProviderAccount) {
	await user.click(listItem(account));
	await waitFor(() => expect(within(detail()).getByRole("heading", { name: account.displayName })).toBeInTheDocument());
}
async function askRemoval(user: User, action: "Sign out" | "Remove") {
	await user.click(within(detail()).getByRole("button", { name: action }));
	return screen.getByRole("group", { name: "Confirm account change" });
}
const confirmButton = (group: HTMLElement) => within(group).getByRole("button", { name: /^(Sign out|Remove)$/ });
async function startAdding(user: User, index = 0, method?: string) {
	await user.click(addButtons()[index]);
	if (method) await user.click(screen.getByRole("button", { name: method }));
	return screen.getByRole("group", { name: /sign-in methods$/ });
}

describe("accounts list and detail", () => {
	it("lists every account by provider and shows the one that is clicked", async () => {
		const user = await start();
		expect(within(screen.getByTestId("provider-section-codex")).getByRole("heading", { name: "Codex" })).toBeInTheDocument();
		expect(within(screen.getByTestId("provider-section-claude")).getByRole("heading", { name: "Claude" })).toBeInTheDocument();
		expect(listItem(alice)).toHaveTextContent(/^Cedar CodexDefault$/);
		expect(listItem(bob)).toHaveTextContent(/^Maple Codex$/);
		expect(listItem(clara)).toHaveTextContent("Willow ClaudeDefault");
		expect(listItem(alice)).toHaveAttribute("aria-current", "true");
		expect(detail()).toHaveTextContent("New Codex sessionsStart on this account.");
		expect(detail()).toHaveTextContent("1 session is using this account.");
		expect(within(detail()).getByText(alice.email)).toHaveClass("blur-sm");
		expect(within(detail()).getByText(alice.email)).toHaveAttribute("tabindex", "0");
		await open(user, bob);
		expect(listItem(bob)).toHaveAttribute("aria-current", "true");
		expect(listItem(alice)).not.toHaveAttribute("aria-current");
		expect(detail()).toHaveTextContent("New Codex sessionsStart on Cedar Codex.");
		await open(user, clara);
		expect(detail()).toHaveTextContent("New Claude Code sessionsStart on this account.");
		expect(detail()).toHaveTextContent("No sessions are using this account.");
	});
	it("marks what the list needs to say about each account", async () => {
		inventory.accounts[0] = { ...alice, global: true, usage: { status: "available", pausedUntil: "2099-01-01T00:00:00Z", signInEnding: true, windows: [{ durationSeconds: 18000, remainingFraction: 0.14 }] } };
		inventory.accounts[1] = { ...bob, kind: "api_key", global: true, reserved: true };
		inventory.accounts[2] = { ...clara, global: true, signedIn: false };
		inventory.accounts.push({ ...bob, id: "d", displayName: "Aspen Codex", usage: { status: "available", windows: [{ durationSeconds: 18000, remainingFraction: 0 }] } });
		const user = await start();
		expect(listItem(alice)).toHaveTextContent(/^Cedar CodexDeviceDefault·Paused·Sign-in ending·14% left$/);
		expect(listItem(bob)).toHaveTextContent(/^Maple CodexDeviceIn reserve·API key$/);
		expect(listItem(clara)).toHaveTextContent(/^Willow ClaudeDeviceSigned out$/);
		expect(screen.getByTestId("provider-account-d")).toHaveTextContent(/^Aspen CodexLimit reached$/);
		// This computer's own login is a blue tag with a laptop beside the name, in the list and above the detail.
		const tags = [within(listItem(alice)).getByText("Device"), within(detail()).getByTestId("provider-account-global")];
		for (const tag of tags) {
			expect(tag).toHaveTextContent(/^Device$/);
			expect(tag).toHaveClass("bg-status-working/15", "text-status-working");
			expect(tag.querySelector("svg.lucide-laptop")).toBeInTheDocument();
			expect(tag).toHaveAttribute("title", "Found in this machine's own sign-in");
		}
		expect(tags[1].previousElementSibling).toHaveTextContent("Cedar Codex");
		expect(within(listItem(alice)).queryByText("Global")).toBeNull();
		await open(user, bob);
		expect(within(listItem(bob)).getByText("Device")).toHaveAttribute("title", "This machine's own API key");
		expect(within(detail()).getByTestId("provider-account-global")).toHaveAttribute("title", "This machine's own API key");
	});
	it("ends each provider's list with its New account row, the only way to add one", async () => {
		await start();
		const rows = (provider: string) => within(screen.getByTestId(`provider-section-${provider}`)).getAllByRole("button").map(row => row.textContent);
		expect(rows("codex")).toEqual(["Cedar CodexDefault", "Maple Codex", "New Codex accountChoose how to sign in."]);
		expect(rows("claude")).toEqual(["Willow ClaudeDefault", "New Claude accountChoose how to sign in."]);
		expect(screen.queryByRole("button", { name: /^Add/ })).toBeNull();
		expect(screen.queryByText(/^Check(ed| again)/)).toBeNull();
	});
	it("checks every account's sign-in on coming back to the window; opening settings did the first check", async () => {
		await start();
		const refreshes = () => mock.get.mock.calls.filter(([, options]) => options?.params?.query?.refresh).length;
		expect(mock.get).toHaveBeenCalledWith(LIST, { params: { query: { includeUsage: true, refresh: false } } });
		expect(mock.get).toHaveBeenCalledWith(LIST, { params: { query: { includeUsage: false, refresh: false } } });
		expect(refreshes()).toBe(0);
		window.dispatchEvent(new Event("focus"));
		await waitFor(() => expect(refreshes()).toBe(1));
		// It happens on its own: the page offers no button for it.
		expect(screen.queryByRole("button", { name: "Check again" })).toBeNull();
	});
	it("shows a catalogue error without pretending that all accounts were signed out", async () => {
		mock.get.mockResolvedValue({ error: { message: "Account catalogue unavailable" } });
		renderAccounts();
		expect(screen.getByText("Loading accounts…")).toBeInTheDocument();
		expect(await screen.findByRole("alert", {}, { timeout: 3000 })).toHaveTextContent("Account catalogue unavailable");
		expect(screen.queryByText(/No signed-in/)).toBeNull();
		expect(screen.queryByTestId("provider-account-a")).toBeNull();
	});
	it("says how to start when a provider has no account", async () => {
		inventory.accounts = [];
		renderAccounts();
		await screen.findByText("No signed-in Codex account.");
		expect(screen.getByText("No signed-in Claude account.")).toBeInTheDocument();
		expect(screen.getAllByText("Managed sessions need you to sign in again.")).toHaveLength(2);
		expect(screen.getByText("No accounts yet")).toBeInTheDocument();
	});
	it("renames an account from its display name field", async () => {
		const user = await start();
		const field = () => within(detail()).getByRole("textbox", { name: "Account name" });
		// Escape puts the saved name back and changes nothing.
		await user.clear(field());
		await user.type(field(), "Scratch{Escape}");
		expect(field()).toHaveValue("Cedar Codex");
		await user.clear(field());
		await user.type(field(), "Work Codex{Enter}");
		expect(actions("rename")).toEqual([["a", { action: "rename", displayName: "Work Codex" }]]);
		expect(await status()).toHaveTextContent("Account name updated.");
		expect(listItem(alice)).toHaveTextContent("Work Codex");
	});
});
describe("account usage", () => {
	it("gives each limit its own row, named by its length or by what it covers, or says why there is none", async () => {
		inventory.accounts[0].usage = { status: "available", plan: "pro", windows: [
			{ durationSeconds: 18000, remainingFraction: 0.75, resetTime: "2030-01-01T00:00:00Z" },
			{ durationSeconds: 604800, remainingFraction: 0 },
			{ scope: "code_review", durationSeconds: 604800, remainingFraction: 0.96, resetTime: "2030-01-01T00:00:00Z" },
			{ scope: "model", name: "GPT-5.3-Codex-Spark", durationSeconds: 18000, remainingFraction: 1 },
		], credits: { balance: "48067.0888145000" } };
		inventory.accounts[1] = { ...bob, kind: "api_key", usage: { status: "unavailable" } };
		inventory.accounts[2].usage = { status: "available", plan: "max", planTier: "20x", windows: [{ durationSeconds: 604800, remainingFraction: 0.34 }], extraUsage: { usedCents: 1820, limitCents: 5000 } };
		inventory.accounts.push({ ...clara, id: "d", displayName: "Pine Claude", primary: false, usage: { status: "unavailable" } });
		const user = await start();
		expect(screen.getByTestId("provider-account-usage-a")).toHaveTextContent(/^5-hourResets Jan 1.*75% left$/);
		expect(screen.getByTestId("provider-account-usage-a-1")).toHaveTextContent("WeeklyReached");
		expect(screen.getByTestId("provider-account-usage-a-2")).toHaveTextContent(/^Code reviewWeekly · Resets Jan 1.*96% left$/);
		expect(screen.getByTestId("provider-account-usage-a-3")).toHaveTextContent("GPT-5.3-Codex-Spark5-hour100% left");
		expect(screen.getAllByRole("progressbar", { name: /alice@example.test/ })).toHaveLength(4);
		expect(detail()).toHaveTextContent("Codex·Pro");
		expect(detail()).toHaveTextContent("CreditsUsed when plan limits run out.48,067 credits");
		expect(listItem(alice)).toHaveTextContent("Default·75% left");
		await open(user, clara);
		expect(detail()).toHaveTextContent("Claude·Max 20x");
		expect(screen.getByTestId("provider-account-usage-c")).toHaveTextContent("Weekly34% left");
		expect(detail()).toHaveTextContent("Extra usage$18.20 of $50.00 this month$31.80 left");
		expect(screen.getByRole("progressbar", { name: "Extra usage" })).toHaveAttribute("aria-valuenow", "64");
		// A provider that will not say, and an API key, which has no limits to report.
		await user.click(screen.getByTestId("provider-account-d"));
		expect(await screen.findByTestId("provider-account-usage-d")).toHaveTextContent("Usage unavailable");
		await open(user, bob);
		expect(screen.getByTestId("provider-account-usage-b")).toHaveTextContent("Not reported for API keys");
		expect(detail()).toHaveTextContent("Codex·API key");
		expect(detail()).toHaveTextContent("Key label");
	});
});
describe("changing the default", () => {
	it("changes the default at once, or keeps it and says why when the change is refused", async () => {
		inventory.accounts[0].sessions = [];
		refusal = "Account is signed out";
		const user = await start();
		expect(within(detail()).queryByRole("button", { name: "Make default" })).toBeNull();
		await open(user, bob);
		await user.click(within(detail()).getByRole("button", { name: "Make default" }));
		expect(await status()).toHaveTextContent("Account is signed out");
		expect(listItem(alice)).toHaveTextContent("Default");
		refusal = "";
		await user.click(within(detail()).getByRole("button", { name: "Make default" }));
		expect(mock.post).toHaveBeenLastCalledWith(ACTIONS, { params: { path: { accountId: "b" } }, body: { action: "primary" } });
		await waitFor(() => expect(listItem(bob)).toHaveTextContent("Default"));
		// The old default had no sessions, so nothing more is asked or said.
		expect(screen.queryByRole("group", { name: "Move sessions" })).toBeNull();
		expect(screen.queryByRole("status")).toBeNull();
	});
	it("offers to move the old default's sessions and moves them one by one", async () => {
		inventory.accounts[0].sessions = ["session-a", "session-d"];
		const user = userEvent.setup();
		const invalidate = vi.spyOn(renderAccounts().cache, "invalidateQueries");
		await open(user, await screen.findByTestId("provider-account-b").then(() => bob));
		await user.click(within(detail()).getByRole("button", { name: "Make default" }));
		const offer = await screen.findByRole("group", { name: "Move sessions" });
		expect(offer).toHaveTextContent("2 sessions still use Cedar Codex");
		expect(actions("assign-session")).toEqual([]);
		// A session that cannot be moved keeps the offer.
		refusal = "Account is signed out";
		await user.click(within(offer).getByRole("button", { name: "Move them here" }));
		expect(await status()).toHaveTextContent("Account is signed out");
		refusal = "";
		await user.click(within(screen.getByRole("group", { name: "Move sessions" })).getByRole("button", { name: "Move them here" }));
		expect(await status()).toHaveTextContent("2 sessions moved to Maple Codex.");
		expect(actions("assign-session").slice(-2)).toEqual([["b", { action: "assign-session", sessionId: "session-a" }], ["b", { action: "assign-session", sessionId: "session-d" }]]);
		expect(screen.queryByRole("group", { name: "Move sessions" })).toBeNull();
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["session-provider-account"] });
	});
	it("leaves the sessions where they are when the offer is dismissed, and moves them at any time", async () => {
		const user = await start();
		await open(user, bob);
		await user.click(within(detail()).getByRole("button", { name: "Make default" }));
		const offer = await screen.findByRole("group", { name: "Move sessions" });
		expect(offer).toHaveTextContent("1 session still uses Cedar Codex");
		await user.click(within(offer).getByRole("button", { name: "Leave it there" }));
		expect(screen.queryByRole("group", { name: "Move sessions" })).toBeNull();
		expect(actions("assign-session")).toEqual([]);
		await open(user, alice);
		await user.click(within(detail()).getByRole("button", { name: "Move sessions" }));
		expect((await screen.findAllByRole("menuitem")).map(option => option.textContent)).toEqual(["Maple CodexDefault"]);
		await user.click(screen.getByRole("menuitem", { name: /Maple Codex/ }));
		expect(await status()).toHaveTextContent("1 session moved to Maple Codex.");
		expect(actions("assign-session")).toEqual([["b", { action: "assign-session", sessionId: "session-a" }]]);
		await waitFor(() => expect(within(detail()).queryByRole("button", { name: "Move sessions" })).toBeNull());
	});
});
describe("account sign-out and removal", () => {
	it("signs out a secondary after saying where its sessions go", async () => {
		const user = await start();
		await open(user, bob);
		const confirm = await askRemoval(user, "Sign out");
		expect(confirm).toHaveTextContent(/^Sign out of Maple Codex\?2 sessions move to Cedar Codex\.CancelSign out$/);
		await user.click(confirmButton(confirm));
		expect(actions("sign-out")).toEqual([["b", { action: "sign-out" }]]);
		expect(await status()).toHaveTextContent("Signed out of Maple Codex.");
		// The account stays in the list, now as a signed-out one that can be removed.
		expect(listItem(bob)).toHaveTextContent("Signed out");
		expect(screen.queryByRole("group", { name: "Confirm account change" })).toBeNull();
		expect(within(detail()).getByRole("button", { name: "Remove" })).toBeInTheDocument();
	});
	it("states one fact at most: nothing without sessions, a wait when the last account goes", async () => {
		inventory.accounts[1].sessions = [];
		inventory.accounts[2].sessions = ["session-z"];
		const user = await start();
		await open(user, bob);
		expect(await askRemoval(user, "Sign out")).toHaveTextContent(/^Sign out of Maple Codex\?CancelSign out$/);
		await open(user, clara);
		const confirm = await askRemoval(user, "Sign out");
		expect(confirm).toHaveTextContent("Sign out of Willow Claude?1 session waits until you sign in again.");
		expect(within(confirm).queryByRole("button", { name: "Replacement default account" })).toBeNull();
		expect(confirmButton(confirm)).toBeEnabled();
	});
	it("signs out the default with the offered replacement, or another the user picks", async () => {
		inventory.accounts.push({ ...bob, id: "d", displayName: "Aspen Codex", signedIn: false, sessions: [] });
		inventory.accounts.push({ ...bob, id: "e", displayName: "Birch Codex", sessions: [], usage: { status: "available", windows: [{ durationSeconds: 18000, remainingFraction: 0.9 }] } });
		const user = await start();
		const confirm = await askRemoval(user, "Sign out");
		expect(confirm).toHaveTextContent("Sign out of Cedar Codex?Choose which account becomes the default.");
		const replacement = within(confirm).getByRole("button", { name: "Replacement default account" });
		expect(replacement).toHaveTextContent(bob.displayName);
		expect(confirmButton(confirm)).toBeEnabled();
		// Only other signed-in accounts of the same provider can take over, each with its room like every account menu.
		await user.click(replacement);
		expect((await screen.findAllByRole("menuitem")).map(option => option.textContent)).toEqual(["Maple Codex", "Birch Codex90% left"]);
		await user.click(screen.getByRole("menuitem", { name: /Birch Codex/ }));
		expect(replacement).toHaveTextContent("Birch Codex");
		await user.click(confirmButton(confirm));
		expect(actions("sign-out")).toEqual([["a", { action: "sign-out", replacementPrimaryId: "e" }]]);
		expect(await status()).toHaveTextContent("Signed out of Cedar Codex.");
	});
	it("removes a signed-out account: nothing when cancelled, kept when refused", async () => {
		inventory.accounts[1].signedIn = false;
		const user = await start();
		await open(user, bob);
		await user.click(within(await askRemoval(user, "Remove")).getByRole("button", { name: "Cancel" }));
		expect(screen.queryByRole("group", { name: "Confirm account change" })).toBeNull();
		expect(mock.post).not.toHaveBeenCalled();
		refusal = "Credential cleanup failed. Retry the operation.";
		await user.click(confirmButton(await askRemoval(user, "Remove")));
		expect(await status()).toHaveTextContent("Credential cleanup failed.");
		expect(listItem(bob)).toBeInTheDocument();
		refusal = "";
		await user.click(confirmButton(screen.getByRole("group", { name: "Confirm account change" })));
		expect(await status()).toHaveTextContent("Removed Maple Codex.");
		expect(actions("remove")).toEqual([["b", { action: "remove" }], ["b", { action: "remove" }]]);
		expect(screen.queryByTestId("provider-account-b")).toBeNull();
	});
});
describe("adding an account", () => {
	it("takes over the detail with the sign-in methods, and keeps the list", async () => {
		const user = await start();
		const group = await startAdding(user);
		expect(group).toHaveAccessibleName("codex sign-in methods");
		expect(within(group).getByRole("heading", { name: "Add a Codex account" })).toBeInTheDocument();
		for (const method of ["Browser", "Device code", "API key"]) expect(within(group).getByRole("button", { name: method })).toBeInTheDocument();
		expect(within(group).getByLabelText("Import JSON")).toBeInTheDocument();
		expect(listItem(alice)).not.toHaveAttribute("aria-current");
		expect(within(screen.getByTestId("provider-section-codex")).getByText("New Codex account")).toBeInTheDocument();
		expect(screen.queryByTestId("provider-account-detail")).toBeNull();
		await user.click(within(group).getByRole("button", { name: "Close" }));
		expect(screen.queryByRole("group", { name: /sign-in methods$/ })).toBeNull();
		expect(detail()).toBeInTheDocument();
		const claude = await startAdding(user, 1);
		expect(within(claude).getByRole("button", { name: "Browser" })).toBeInTheDocument();
		expect(within(claude).queryByRole("button", { name: "Device code" })).toBeNull();
		// A sign-in that cannot start says so and leaves Add account available.
		loginStart = { error: { message: "Login callback port is in use; retry later" } };
		await user.click(within(claude).getByRole("button", { name: "Browser" }));
		expect(await status()).toHaveTextContent("Login callback port is in use");
		expect(addButtons()[1]).toBeEnabled();
	});
	it("keeps the provider link in the panel until the user opens it", async () => {
		const user = await start();
		const group = await startAdding(user, 0, "Browser");
		expect(mock.post).toHaveBeenCalledWith(LOGIN, { body: { provider: "codex", mode: "browser" } });
		expect(await within(group).findByText("Complete sign-in in your browser.")).toBeInTheDocument();
		// The sign-in under way keeps its own row; no other can be started.
		expect(addButtons().map(button => button.disabled)).toEqual([false, true]);
		expect(mock.open).not.toHaveBeenCalled();
		await user.click(within(group).getByRole("button", { name: "Copy link" }));
		expect(mock.clipboard).toHaveBeenCalledWith("https://provider.test/login");
		// A browser that cannot be opened says so and leaves the link in reach.
		mock.open.mockRejectedValueOnce(new Error("Browser could not be opened"));
		await user.click(screen.getByRole("button", { name: "Open sign-in page" }));
		expect(await status()).toHaveTextContent("Browser could not be opened");
		await user.click(screen.getByRole("button", { name: "Open sign-in page" }));
		expect(mock.open).toHaveBeenLastCalledWith("https://provider.test/login");
		// Another account can be viewed meanwhile; the sign-in keeps its place in the list.
		await open(user, bob);
		await user.click(within(screen.getByTestId("provider-section-codex")).getByText("Signing in"));
		await user.click(await screen.findByRole("button", { name: "Cancel sign-in" }));
		expect(mock.remove).toHaveBeenCalledWith("/api/v1/provider-accounts/login/{loginId}", { params: { path: { loginId: "login-1" } } });
		await waitFor(() => expect(screen.queryByText("Complete sign-in in your browser.")).toBeNull());
		expect(mock.post).toHaveBeenCalledTimes(1);
	});
	it("starts Codex device login and keeps its code visible", async () => {
		loginStart = loginStatus = { data: waitingLogin({ mode: "device", code: "ABCD-EFGH", url: "https://provider.test/device" }) };
		const user = await start();
		const group = await startAdding(user, 0, "Device code");
		expect(mock.post).toHaveBeenCalledWith(LOGIN, { body: { provider: "codex", mode: "device" } });
		expect(await within(group).findByText(/ABCD-EFGH/)).toBeInTheDocument();
		expect(group).toHaveTextContent("Enter this code on the Codex sign-in page.");
		await user.click(within(group).getByRole("button", { name: "Copy code" }));
		expect(mock.clipboard).toHaveBeenCalledWith("ABCD-EFGH");
		expect(mock.open).not.toHaveBeenCalled();
		await user.click(within(group).getByRole("button", { name: "Open sign-in page" }));
		expect(mock.open).toHaveBeenCalledWith("https://provider.test/device");
	});
	it("opens the API key form under its own row and sends the key without rendering it", async () => {
		loginStart = loginStatus = { data: waitingLogin({ mode: "api_key", url: undefined }) };
		const user = await start();
		const group = await startAdding(user, 1);
		const row = within(group).getByRole("button", { name: "API key" });
		await user.click(row);
		expect(row).toHaveAttribute("aria-expanded", "true");
		await user.click(row);
		expect(within(group).queryByRole("button", { name: "Add" })).toBeNull();
		await user.click(row);
		// Each box names itself and shows what a real value looks like; the key is hidden until the eye is pressed.
		const key = within(group).getByPlaceholderText("API key (sk-ant-…)");
		expect(within(group).getByRole("button", { name: "Add" })).toBeDisabled();
		await user.type(key, "secret-api-key");
		await user.type(within(group).getByPlaceholderText("Base URL (https://api.anthropic.com)"), "https://api.example.test");
		expect(within(group).getByRole("textbox", { name: "Display label (optional)" })).toHaveValue("");
		expect(key).toHaveAttribute("type", "password");
		await user.click(within(group).getByRole("button", { name: "Show API key" }));
		expect(key).toHaveAttribute("type", "text");
		await user.click(within(group).getByRole("button", { name: "Hide API key" }));
		expect(key).toHaveAttribute("type", "password");
		await user.click(within(group).getByRole("button", { name: "Add" }));
		expect(mock.post).toHaveBeenCalledWith(LOGIN, { body: { provider: "claude", mode: "api_key", apiKey: "secret-api-key", baseUrl: "https://api.example.test" } });
		expect(screen.queryByText("secret-api-key")).toBeNull();
	});
	it("reads a JSON file and sends its contents, unless it is too large", async () => {
		loginStart = loginStatus = { data: waitingLogin({ mode: "import", url: undefined }) };
		const user = await start();
		const input = within(await startAdding(user)).getByLabelText("Import JSON");
		await user.upload(input, new File(["x".repeat((1 << 20) + 1)], "big.json", { type: "application/json" }));
		expect(await status()).toHaveTextContent("Credential JSON exceeds 1 MiB.");
		expect(mock.post).not.toHaveBeenCalled();
		const credential = JSON.stringify({ type: "codex", email: "imported@example.test", access_token: "secret" });
		await user.upload(input, new File([credential], "codex.json", { type: "application/json" }));
		await waitFor(() => expect(mock.post).toHaveBeenCalledWith(LOGIN, { body: { provider: "codex", mode: "import", credentialJson: credential } }));
	});
});
describe("a sign-in that is waiting", () => {
	const polls = () => mock.get.mock.calls.filter(([path]) => path === "/api/v1/provider-accounts/login/{loginId}");
	it("is still there after leaving and returning to settings", async () => {
		const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
		const user = userEvent.setup();
		const { view } = renderAccounts(cache);
		await screen.findByTestId("provider-account-a");
		await startAdding(user, 0, "Browser");
		await screen.findByText("Complete sign-in in your browser.");
		view.unmount();
		renderAccounts(cache);
		expect(screen.getByText("Complete sign-in in your browser.")).toBeInTheDocument();
		// The sign-in under way keeps its own row; no other can be started.
		expect(addButtons().map(button => button.disabled)).toEqual([false, true]);
		// A cancellation that is refused keeps the attempt.
		mock.remove.mockResolvedValueOnce({ error: { message: "Unable to cancel login. Try again." } });
		await user.click(screen.getByRole("button", { name: "Cancel sign-in" }));
		expect(await status()).toHaveTextContent("Unable to cancel login. Try again.");
		expect(cache.getQueryData(loginCacheKey)).toMatchObject({ id: "login-1", status: "waiting" });
		await user.click(screen.getByRole("button", { name: "Cancel sign-in" }));
		await waitFor(() => expect(screen.queryByText("Complete sign-in in your browser.")).toBeNull());
		expect(cache.getQueryData(loginCacheKey)).toBeNull();
		expect(mock.post).toHaveBeenCalledTimes(1);
	});
	it("shows the account that was signed in, and reads the accounts again", async () => {
		loginStart = { data: waitingLogin({ provider: "claude" }) };
		loginStatus = { data: waitingLogin({ provider: "claude", status: "complete", accountId: "c" }) };
		const user = userEvent.setup();
		const { cache } = renderAccounts();
		await screen.findByTestId("provider-account-a");
		const reads = mock.get.mock.calls.length;
		await startAdding(user, 1, "Browser");
		expect(await status()).toHaveTextContent("Account signed in.");
		expect(polls()[0][1]).toEqual({ params: { path: { loginId: "login-1" } } });
		expect(within(detail()).getByRole("heading", { name: clara.displayName })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Open sign-in page" })).toBeNull();
		expect(addButtons().every(button => !button.disabled)).toBe(true);
		expect(cache.getQueryData(loginCacheKey)).toMatchObject({ status: "complete", accountId: "c" });
		await waitFor(() => expect(mock.get.mock.calls.filter(([path]) => path === LIST).length).toBeGreaterThan(reads));
	});
	it("keeps the attempt while its state cannot be read, and says so when the provider reports failure", async () => {
		loginStatus = { error: { message: "AO is reconnecting" } };
		const user = userEvent.setup();
		const { cache } = renderAccounts();
		await screen.findByTestId("provider-account-a");
		await startAdding(user, 0, "Browser");
		await waitFor(() => expect(polls().length).toBeGreaterThan(0));
		expect(cache.getQueryData(loginCacheKey)).toMatchObject({ status: "waiting" });
		expect(screen.getByRole("button", { name: "Cancel sign-in" })).toBeEnabled();
		loginStatus = { data: waitingLogin({ status: "failed" }) };
		expect(await screen.findByRole("status", {}, { timeout: 4000 })).toHaveTextContent("Sign-in failed. Please sign in again.");
		expect(screen.queryByRole("button", { name: "Open sign-in page" })).toBeNull();
		expect(detail()).toBeInTheDocument();
	});
	it("releases an attempt the daemon no longer knows, so a fresh sign-in can start", async () => {
		const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		cache.setQueryData(loginCacheKey, waitingLogin({ id: "old-attempt" }));
		loginStatus = { error: { code: "PROVIDER_LOGIN_NOT_FOUND", message: "Login attempt not found" } };
		renderAccounts(cache);
		expect(await status()).toHaveTextContent("Sign-in failed. Please sign in again.");
		expect(cache.getQueryData(loginCacheKey)).toMatchObject({ id: "old-attempt", status: "failed" });
		expect(addButtons().every(button => !button.disabled)).toBe(true);
		expect(mock.remove).not.toHaveBeenCalled();
		loginStart = loginStatus = { data: waitingLogin({ id: "new-attempt" }) };
		await startAdding(userEvent.setup(), 0, "Browser");
		await waitFor(() => expect(cache.getQueryData(loginCacheKey)).toMatchObject({ id: "new-attempt", status: "waiting" }));
	});
});
describe("signing in again", () => {
	it("is the first thing on a signed-out account's page, and runs in place", async () => {
		inventory.accounts[1] = { ...bob, signedIn: false, usage: { status: "available", windows: [{ durationSeconds: 18000, remainingFraction: 0.5 }] } };
		loginStart = loginStatus = { data: waitingLogin({ accountId: "b" }) };
		const user = await start();
		await open(user, bob);
		expect(detail()).toHaveTextContent("Codex·Signed out");
		expect(detail()).toHaveTextContent("This account is signed out2 sessions are waiting for it.");
		expect(screen.queryByTestId("provider-account-usage-b")).toBeNull();
		expect(within(detail()).queryByRole("button", { name: "Make default" })).toBeNull();
		expect(within(detail()).queryByRole("button", { name: "Manage plan" })).toBeNull();
		expect(within(detail()).getByRole("button", { name: "Remove" })).toBeInTheDocument();
		expect(within(detail()).queryByRole("button", { name: "Sign out" })).toBeNull();
		await user.click(within(detail()).getByRole("button", { name: "Sign in again" }));
		expect(mock.post).toHaveBeenCalledWith(LOGIN, { body: { provider: "codex", accountId: "b" } });
		expect(await within(detail()).findByText("Complete sign-in in your browser.")).toBeInTheDocument();
		expect(within(detail()).getByRole("button", { name: "Open sign-in page" })).toBeEnabled();
		expect(mock.open).not.toHaveBeenCalled();
	});
	it("is offered above the limits of an account whose sign-in has stopped renewing", async () => {
		inventory.accounts[0].usage = { status: "available", signInEnding: true, signInEndsAt: "2099-01-01T00:00:00Z", windows: [{ durationSeconds: 18000, remainingFraction: 0.14 }] };
		inventory.accounts[1].usage = { status: "available", signInEnding: true, windows: [] };
		loginStart = loginStatus = { data: waitingLogin({ accountId: "b" }) };
		const user = await start();
		const warning = (account: ProviderAccount) => screen.getByTestId(`provider-account-sign-in-ending-${account.id}`);
		expect(warning(alice)).toHaveTextContent(/Sign-in is not renewingWorks until Jan 1.*Sign in again to keep using it\.Sign in again/);
		expect(detail()).toHaveTextContent("14% left");
		await open(user, bob);
		expect(warning(bob)).toHaveTextContent("It will stop working. Sign in again to keep using it.");
		await user.click(within(warning(bob)).getByRole("button", { name: "Sign in again" }));
		expect(mock.post).toHaveBeenCalledWith(LOGIN, { body: { provider: "codex", accountId: "b" } });
		expect(await within(warning(bob)).findByText("Complete sign-in in your browser.")).toBeInTheDocument();
		await open(user, clara);
		expect(screen.queryByTestId("provider-account-sign-in-ending-c")).toBeNull();
	});
});
describe("account resets", () => {
	const limited = () => ({ status: "available" as const, plan: "pro", resetCredits: 2, resetUsable: true, resets: [{ left: 1, total: 1, expiresAt: "2030-01-21T00:00:00Z" }, { label: "Launch bonus", left: 2, total: 3, expiresAt: "2030-02-04T00:00:00Z" }], windows: [{ durationSeconds: 18000, remainingFraction: 0 }] });
	const ask = () => screen.getByRole("group", { name: "Use reset" });
	it("lists each reset with its expiry and asks once before spending one", async () => {
		inventory.accounts[0].usage = limited();
		const user = await start();
		expect(detail()).toHaveTextContent("Limit resetsClears the usage limits.2 availableUse reset");
		expect(detail()).toHaveTextContent("Reset 1Expires Jan 21, 2030");
		expect(detail()).toHaveTextContent("Launch bonus · 2 of 3 leftExpires Feb 4, 2030");
		await user.click(within(detail()).getByRole("button", { name: "Use reset" }));
		expect(ask()).toHaveTextContent("Use a reset on Cedar Codex?Clears the usage limits. This can't be undone.");
		await user.click(within(ask()).getByRole("button", { name: "Cancel" }));
		expect(screen.queryByRole("group", { name: "Use reset" })).toBeNull();
		expect(actions("reset")).toEqual([]);
		const refreshes = mock.get.mock.calls.filter(([, options]) => options?.params?.query?.refresh).length;
		await user.click(within(detail()).getByRole("button", { name: "Use reset" }));
		await user.click(within(ask()).getByRole("button", { name: "Use reset" }));
		expect(await screen.findByText("Limits reset on Cedar Codex.")).toBeInTheDocument();
		expect(actions("reset")).toEqual([["a", { action: "reset" }]]);
		// Usage is read again straight away, so the bars show the new room.
		await waitFor(() => expect(mock.get.mock.calls.filter(([, options]) => options?.params?.query?.refresh).length).toBe(refreshes + 1));
		// Every other outcome is stated once too, and nothing is tried again.
		const outcomes = { nothing_to_reset: "No limit to reset right now.", none_available: "No reset is available.", wait: "The provider isn't accepting a reset yet. Try again later.", failed: "The reset didn't go through.", unknown: "The provider didn't confirm the reset. Check usage before trying again." };
		for (const [outcome, message] of Object.entries(outcomes)) {
			resetOutcome = outcome;
			await user.click(within(detail()).getByRole("button", { name: "Use reset" }));
			await user.click(within(ask()).getByRole("button", { name: "Use reset" }));
			expect(await screen.findByText(message)).toBeInTheDocument();
		}
		expect(actions("reset")).toHaveLength(6);
	});
	it("says when the next reset is allowed, takes one before the limit is reached, and shows no resets as none", async () => {
		const pine = { ...clara, id: "d", displayName: "Pine Claude", primary: false };
		const fir = { ...clara, id: "e", displayName: "Fir Claude", primary: false, kind: "api_key" as const };
		inventory.accounts[0].usage = { ...limited(), resetUsable: false, resetBlockedUntil: "2099-01-01T00:00:00Z" };
		inventory.accounts[1].usage = { status: "available", resetCredits: 0, windows: [{ durationSeconds: 18000, remainingFraction: 0.5 }] };
		inventory.accounts[2].usage = { ...limited(), resetUsable: false, windows: [{ durationSeconds: 18000, remainingFraction: 0.6 }] };
		inventory.accounts.push({ ...pine, usage: { status: "available", windows: [{ durationSeconds: 18000, remainingFraction: 0.5 }] } }, { ...fir, usage: { status: "unavailable" } });
		const user = await start();
		expect(detail()).toHaveTextContent(/Limit resetsNext reset after Jan 1.*2 availableUse reset/);
		expect(within(detail()).getByRole("button", { name: "Use reset" })).toBeDisabled();
		await open(user, bob);
		expect(detail()).toHaveTextContent("ResetsNo resets available");
		expect(within(detail()).queryByRole("button", { name: "Use reset" })).toBeNull();
		await open(user, clara);
		expect(detail()).toHaveTextContent("Limit resetsUse one when a limit is reached.2 availableUse reset");
		// No wait is in force, so a reset can be spent early; the confirmation says the limit is not reached.
		await user.click(within(detail()).getByRole("button", { name: "Use reset" }));
		expect(ask()).toHaveTextContent("Use a reset on Willow Claude?The limit is not reached yet. This can't be undone.");
		await user.click(within(ask()).getByRole("button", { name: "Use reset" }));
		expect(await screen.findByText("Limits reset on Willow Claude.")).toBeInTheDocument();
		expect(actions("reset")).toEqual([["c", { action: "reset" }]]);
		// A sign-in whose provider reports no resets says so too; an API key has none to speak of.
		await open(user, pine);
		expect(detail()).toHaveTextContent("ResetsNo resets available");
		await open(user, fir);
		expect(detail()).not.toHaveTextContent("No resets available");
	});
});
describe("a paused account", () => {
	it("says so in its limits and can be resumed, and says nothing once the pause is over", async () => {
		inventory.accounts[0].usage = { status: "available", pausedUntil: "2099-01-01T00:00:00Z", pausedReason: "quota", windows: [{ durationSeconds: 18000, remainingFraction: 0.14 }] };
		inventory.accounts[1].usage = { status: "available", pausedUntil: "2001-01-01T00:00:00Z", windows: [{ durationSeconds: 18000, remainingFraction: 0.14 }] };
		const user = await start();
		expect(detail()).toHaveTextContent(/Paused until Jan 1.*The provider rate-limited this account\.Resume now/);
		await user.click(within(detail()).getByRole("button", { name: "Resume now" }));
		expect(await screen.findByText("Cedar Codex resumed.")).toBeInTheDocument();
		expect(actions("resume")).toEqual([["a", { action: "resume" }]]);
		await open(user, bob);
		expect(listItem(bob)).not.toHaveTextContent("Paused");
		expect(within(detail()).queryByRole("button", { name: "Resume now" })).toBeNull();
	});
});
describe("plan and activity", () => {
	const quiet = Array.from({ length: 18 }, () => ({ succeeded: 0, failed: 0 }));
	const aside = () => within(detail()).getByRole("complementary");
	it("puts the plan facts and recent requests beside the limits", async () => {
		inventory.accounts[0].usage = { status: "available", windows: [{ durationSeconds: 18000, remainingFraction: 0.5 }] };
		inventory.accounts[1] = { ...bob, kind: "api_key", usage: { status: "unavailable", addedAt: "2030-09-28T00:00:00Z", requests: [...quiet, { succeeded: 2, failed: 0 }, { succeeded: 1, failed: 0 }] } };
		inventory.accounts[2].usage = { status: "available", plan: "team", organization: "Acme", renewsAt: "2030-11-14T00:00:00Z", addedAt: "2030-09-03T00:00:00Z", refreshedAt: new Date(Date.now() - 12 * 60_000).toISOString(), requests: [...quiet, { succeeded: 9, failed: 0 }, { succeeded: 4, failed: 3 }], windows: [{ durationSeconds: 18000, remainingFraction: 0.5 }], models: ["opus", "sonnet", "haiku", "opus-4", "sonnet-4", "haiku-4", "opus-3", "haiku-3"] };
		const user = await start();
		expect(within(detail()).queryByRole("complementary")).toBeNull();
		// Each provider keeps the plan, its usage and its billing on a page of its own.
		await user.click(within(detail()).getByRole("button", { name: "Manage plan" }));
		expect(mock.open).toHaveBeenLastCalledWith("https://chatgpt.com/codex/settings/usage");
		await open(user, clara);
		await user.click(within(detail()).getByRole("button", { name: "Manage plan" }));
		expect(mock.open).toHaveBeenLastCalledWith("https://claude.ai/settings/usage");
		for (const fact of ["PlanTeam", "RenewsNov 14, 2030", "OrganizationAcme", "AddedSep 3, 2030", "RequestsLast 3 hours163 failed"]) expect(aside()).toHaveTextContent(fact);
		// The models it can use: six by name, the rest counted and named on pointing.
		expect(aside()).toHaveTextContent("Modelsopussonnethaikuopus-4sonnet-4haiku-4+2");
		expect(within(aside()).getByText("+2")).toHaveAttribute("title", "opus-3, haiku-3");
		// Renewing the sign-in is a row among the account's own, which says what it does and when it last happened.
		expect(detail()).toHaveTextContent(/AccountDisplay name.*Signed in as.*Renew sign-inGets a fresh login from Claude\. Last renewed 12 minutes ago\.RenewSign out/);
		await user.click(within(detail()).getByRole("button", { name: "Renew" }));
		expect(await screen.findByText("Sign-in renewed.")).toBeInTheDocument();
		expect(actions("refresh-sign-in")).toEqual([["c", { action: "refresh-sign-in" }]]);
		// An API key has no limits and no sign-in to renew, but its activity is known.
		await open(user, bob);
		expect(aside()).toHaveTextContent("AddedSep 28, 2030");
		expect(aside()).toHaveTextContent("RequestsLast 3 hours3None failed");
		expect(within(detail()).queryByRole("button", { name: "Renew" })).toBeNull();
		expect(within(detail()).queryByRole("button", { name: "Manage plan" })).toBeNull();
	});
	it("shows the plan and what the helper counted even while the limits are unavailable", async () => {
		inventory.accounts[0].usage = { status: "unavailable", plan: "pro", activity: {
			today: 1_240, week: 5_400, total: 98_000, since: "2030-02-01",
			days: [{ date: "2030-02-01", tokens: 600 }, { date: "2030-02-02", tokens: 0 }, { date: "2030-02-03", tokens: 1_200 }],
			models: [{ model: "gpt-5.3-codex", tokens: 750 }, { model: "gpt-5.2", tokens: 250 }],
		} };
		inventory.accounts[1].usage = { status: "unavailable", activity: { today: 0, week: 0, total: 0 } };
		mock.costs = new Map([["session-a", { estimatedCost: { totalNanos: 2_500_000_000 } }]]);
		const user = await start();
		expect(screen.getByTestId("provider-account-usage-a")).toHaveTextContent("Usage unavailable");
		expect(detail()).toHaveTextContent("Codex·Pro");
		expect(aside()).toHaveTextContent("PlanPro");
		// A bar a day, the busiest the tallest and today's the last, then the totals and what its sessions cost.
		const bars = within(aside()).getByRole("img", { name: "Tokens per day" });
		expect(Array.from(bars.querySelectorAll<HTMLElement>("[title]"), bar => [bar.title, bar.style.height])).toEqual([["Feb 1 · 600", "50%"], ["Feb 2 · 0", "0%"], ["Feb 3 · 1.2K", "100%"]]);
		expect(bars.querySelector("[title^='Feb 3']")).toHaveClass("bg-foreground");
		expect(bars).toHaveTextContent("Feb 1Tokens per dayToday");
		expect(aside()).toHaveTextContent("Through AO today1.2KThrough AO, 7 days5.4KThrough AO since Feb 198KEstimated cost of its sessions$2.50");
		expect(within(aside()).getByText("Estimated cost of its sessions")).toHaveAttribute("title", "Token counts priced with AO's price list. A subscription is not billed this way.");
		expect(aside()).toHaveTextContent("By model, 7 daysgpt-5.3-codex75%gpt-5.225%");
		// A quiet account has totals but no bars, no cost and no model split.
		await open(user, bob);
		expect(aside()).toHaveTextContent(/^ActivityThrough AO today0Through AO, 7 days0$/);
		expect(within(aside()).queryByRole("img")).toBeNull();
	});
	it("adds the provider's token tally, leaving out the figures it does not report", async () => {
		const today = new Date().toISOString().slice(0, 10);
		inventory.accounts[0].usage = { status: "available", plan: "pro", windows: [], requests: Array.from({ length: 20 }, () => ({ succeeded: 0, failed: 0 })), tokens: { latestDay: today, latestDayTokens: 1_240_000, lifetime: 482_000_000, peakDaily: 9_400_000, longestTurnSeconds: 4_320, currentStreakDays: 6, longestStreakDays: 23 } };
		inventory.accounts[1].usage = { status: "available", windows: [], tokens: { latestDay: "2030-01-05", latestDayTokens: 3_800, lifetime: 96_000_000 } };
		const user = await start();
		// A quiet account says so in figures, and renewing stays in reach when its last time is not known.
		for (const fact of ["RequestsLast 3 hours0None failed", "Tokens today1.2M", "Lifetime tokens482M", "Peak day9.4M", "Longest turn1 hr 12 min", "Current streak6 days", "Longest streak23 days"]) expect(aside()).toHaveTextContent(fact);
		expect(within(detail()).getByRole("button", { name: "Renew" })).toBeEnabled();
		expect(detail()).not.toHaveTextContent("Last renewed");
		// A tally whose latest day is not today names the day; with nothing from the helper there is no sign-in row.
		await open(user, bob);
		expect(aside()).toHaveTextContent("Tokens on Jan 5, 20303.8K");
		expect(aside()).toHaveTextContent("Lifetime tokens96M");
		expect(aside()).not.toHaveTextContent(/Peak day|Requests/);
		expect(within(detail()).queryByRole("button", { name: "Renew" })).toBeNull();
	});
});
describe("account health", () => {
	it("says how the last failure went, counts the recent ones by kind, and gives the time to the first word", async () => {
		inventory.accounts[0].usage = { status: "unavailable", health: { lastFailure: { kind: "sign-in", at: "2030-03-04T12:00:00Z" }, failures: { limit: 3, signIn: 1, server: 0 }, firstWordMs: 1840 } };
		inventory.accounts[1].usage = { status: "available", windows: [], health: { firstWordMs: 900 } };
		const user = await start();
		expect(detail()).toHaveTextContent(/HealthLast failureSign-in refused · Mar 4, 2030, \d+:\d\d.*M3 limit reached1 sign-in refusedTime to first wordTypical, last hour1\.8 s/);
		await open(user, bob);
		expect(detail()).toHaveTextContent("HealthTime to first wordTypical, last hour0.9 s");
		expect(detail()).not.toHaveTextContent("Last failure");
		await open(user, clara);
		expect(detail()).not.toHaveTextContent("Health");
	});
});
describe("the sessions on an account", () => {
	const session = (id: string, title: string, status = "idle") => ({ id, title, status });
	const list = () => screen.getByTestId("provider-account-sessions");
	const row = (name: string) => within(within(list()).getByText(name).parentElement!);
	beforeEach(() => {
		mock.workspaces = [
			{ id: "project-1", sessions: [session("s1", "Fix login", "working"), session("s2", "Write docs"), session("s3", ""), session("s4", "Tidy"), session("s5", "Bench"), session("s6", "Lint")] },
			{ id: "__standalone__", sessions: [session("s7", "Scratch")] },
		];
		inventory.accounts[0].sessions = ["s1", "s2", "s3", "s4", "s5", "s6", "s7", "gone"];
		inventory.accounts[0].usage = { status: "available", windows: [], activity: { today: 49_200, week: 49_200, total: 49_200, sessions: { s7: 48_000, s2: 1_200 } } };
	});
	it("names them, the busiest today first and five at a time", async () => {
		const user = await start();
		expect(mock.workspaceQuery).toHaveBeenCalledWith({ includeCloud: false });
		// A session AO no longer knows is left out of the list and of its count.
		expect(detail()).toHaveTextContent("7 sessions on this account");
		expect(list()).toHaveTextContent(/^Scratch48K todayWrite docs1\.2K todayFix logins3Tidy2 more$/);
		expect(row("Fix login").getByText("Fix login").previousElementSibling).toHaveClass("bg-status-working");
		expect(row("Tidy").getByText("Tidy").previousElementSibling).not.toHaveClass("bg-status-working");
		await user.click(within(list()).getByRole("button", { name: "2 more" }));
		expect(list()).toHaveTextContent(/^Scratch48K todayWrite docs1\.2K todayFix logins3TidyBenchLint$/);
		// An account with no session AO knows has no list.
		await open(user, clara);
		expect(screen.queryByTestId("provider-account-sessions")).toBeNull();
	});
	it("moves one to another account, or opens it and leaves settings", async () => {
		const user = await start();
		await user.click(row("Scratch").getByRole("button", { name: "Move to another account" }));
		expect((await screen.findAllByRole("menuitem")).map(option => option.textContent)).toEqual(["Maple Codex"]);
		await user.click(screen.getByRole("menuitem", { name: "Maple Codex" }));
		expect(await status()).toHaveTextContent("Session moved to Maple Codex.");
		expect(actions("assign-session")).toEqual([["b", { action: "assign-session", sessionId: "s7" }]]);
		await waitFor(() => expect(detail()).toHaveTextContent("6 sessions on this account"));
		useUiStore.getState().openGlobalSettings("accountManager");
		await user.click(row("Fix login").getByRole("button", { name: "Open session" }));
		expect(mock.navigate).toHaveBeenLastCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "project-1", sessionId: "s1" } });
		expect(useUiStore.getState().settingsModal).toBeNull();
		await open(user, bob);
		await user.click(row("Scratch").getByRole("button", { name: "Open session" }));
		expect(mock.navigate).toHaveBeenLastCalledWith({ to: "/sessions/$sessionId", params: { sessionId: "s7" } });
	});
});
describe("what an account does on its own", () => {
	const setting = () => actions("settings").at(-1);
	const options = async () => (await screen.findAllByRole("menuitem")).map(option => option.textContent);
	it("keeps an account in reserve, hands its sessions on at a limit, and warns when little is left", async () => {
		const user = await start();
		// The default always takes new sessions.
		expect(within(detail()).getByRole("switch", { name: "Use for new sessions" })).toBeDisabled();
		expect(detail()).toHaveTextContent("Use for new sessionsThe default is always used.");
		await open(user, bob);
		const reserve = () => within(detail()).getByRole("switch", { name: "Use for new sessions" });
		expect(reserve()).toBeChecked();
		expect(detail()).toHaveTextContent("Use for new sessionsOff keeps the account in reserve.");
		await user.click(reserve());
		expect(setting()).toEqual(["b", { action: "settings", reserved: true }]);
		await waitFor(() => expect(listItem(bob)).toHaveTextContent(/^Maple CodexIn reserve$/));
		expect(reserve()).not.toBeChecked();
		await user.click(reserve());
		expect(setting()).toEqual(["b", { action: "settings", reserved: false }]);
		expect(detail()).toHaveTextContent("When a limit is reachedIts sessions move on their next request.Do nothing");
		await user.click(within(detail()).getByRole("button", { name: "Do nothing" }));
		expect(await options()).toEqual(["Do nothing", "Cedar Codex"]);
		await user.click(screen.getByRole("menuitem", { name: "Cedar Codex" }));
		expect(setting()).toEqual(["b", { action: "settings", onLimit: "a" }]);
		await user.click(await within(detail()).findByRole("button", { name: "Switch to Cedar Codex" }));
		await user.click(await screen.findByRole("menuitem", { name: "Do nothing" }));
		expect(setting()).toEqual(["b", { action: "settings", onLimit: "" }]);
		await user.click(await within(detail()).findByRole("button", { name: "Never" }));
		expect(await options()).toEqual(["Never", "At 5%", "At 10%", "At 20%", "At 30%"]);
		await user.click(screen.getByRole("menuitem", { name: "At 20%" }));
		expect(setting()).toEqual(["b", { action: "settings", warnAt: 20 }]);
		expect(await within(detail()).findByRole("button", { name: "At 20%" })).toHaveTextContent("At 20%");
		// With no other account to hand its sessions to, that choice is not offered.
		await open(user, clara);
		expect(detail()).not.toHaveTextContent("When a limit is reached");
		expect(detail()).toHaveTextContent("Warn me when little is leftNever");
	});
});
