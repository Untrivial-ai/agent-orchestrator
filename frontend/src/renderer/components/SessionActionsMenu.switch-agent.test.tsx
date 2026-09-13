import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { SessionActionsMenu } from "./SessionActionsMenu";
import { SwitchAgentDialog } from "./SwitchAgentDialog";
import { TerminalSwitchAgentButton } from "./TerminalSwitchAgentButton";
import { TooltipProvider } from "./ui/tooltip";

// Regression test for: clicking "Switch agent" in the session actions dropdown
// opened SwitchAgentDialog and then immediately closed it again in the same
// tick. Root cause: DropdownMenuItem's onSelect closes the parent
// DropdownMenu (Radix default, no preventDefault), and SwitchAgentDialog is
// non-modal (Dialog modal={false}), so its Radix DismissableLayer treats the
// residual pointer/focus activity from the closing dropdown as an
// outside-interaction and dismisses the dialog right after it opens.
//
// Unlike TerminalSwitchAgentButton.test.tsx and SwitchAgentDialog.test.tsx,
// this test drives the *real* DropdownMenuItem click path (variant="menu-item"
// inside a real SessionActionsMenu/DropdownMenu) instead of mocking the
// button out or opening the dialog directly with open=true.

const { getMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: getMock,
		POST: postMock,
	},
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
}));

const worker: WorkspaceSession = {
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	branch: "ao/sess-1",
	id: "sess-1",
	kind: "worker",
	provider: "claude-code",
	prs: [],
	status: "working",
	title: "do the thing",
	updatedAt: "2026-06-10T00:00:00Z",
	workspaceId: "proj-1",
	workspaceName: "my-app",
};

function SessionActionsMenuHarness() {
	const [container, setContainer] = useState<HTMLDivElement | null>(null);
	const [open, setOpen] = useState(false);
	return (
		<div className="relative" data-testid="terminal-container" ref={setContainer}>
			<SessionActionsMenu>
				<TerminalSwitchAgentButton
					container={container}
					onOpenChange={setOpen}
					open={open}
					session={worker}
					switchError={null}
					variant="menu-item"
				/>
			</SessionActionsMenu>
			{container ? (
				<SwitchAgentDialog container={container} onOpenChange={setOpen} open={open} session={worker} />
			) : null}
		</div>
	);
}

function renderHarness() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<SessionActionsMenuHarness />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	getMock.mockReset();
	getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
		if (path === "/api/v1/agents/{agent}/models") {
			const agentId = options?.params?.path?.agent ?? "codex";
			return {
				data: {
					agentId,
					allowCustom: false,
					fetchedAt: "2026-06-10T00:00:00Z",
					models: [{ id: agentId === "codex" ? "gpt-5.4" : "claude-opus-4-6", label: "Default" }],
					selectionMode: "catalog",
					source: "test",
					stale: false,
				},
				error: undefined,
				response: { status: 200 },
			};
		}
		return { data: { switches: [] }, error: undefined, response: { status: 200 } };
	});
	postMock.mockReset();
});

// JSDOM reports animationName "none" everywhere, so Radix Presence unmounts a
// closing menu immediately instead of retaining it for the 100ms
// `animate-popover-out` exit — which is exactly the real-renderer window this
// suite exists to cover. Make the exit visible to Presence only, scoped to
// menu content and derived from its own `data-state` just like the CSS pair
// `data-[state=open]:animate-popover-in data-[state=closed]:animate-popover-out`.
// Presence needs the two names to DIFFER: it compares the name it saw while
// mounted against the one at close time to decide an animation is running.
// Scoped to this file on purpose: a global stub would flip every menu in every
// suite into retained-until-animationend mode.
function fakeMenuExitAnimation(): () => void {
	const original = window.getComputedStyle;
	const real = original.bind(window);
	Object.defineProperty(window, "getComputedStyle", {
		configurable: true,
		writable: true,
		value: (element: Element, pseudo?: string | null) => {
			const styles = real(element, pseudo);
			if (element.getAttribute("role") !== "menu") return styles;
			return new Proxy(styles, {
				get(target, property) {
					if (property === "animationName") {
						return element.getAttribute("data-state") === "closed"
							? "animate-popover-out"
							: "animate-popover-in";
					}
					const value = Reflect.get(target, property, target);
					return typeof value === "function" ? (value as () => unknown).bind(target) : value;
				},
			}) as CSSStyleDeclaration;
		},
	});
	return () => {
		Object.defineProperty(window, "getComputedStyle", {
			configurable: true,
			writable: true,
			value: original,
		});
	};
}

// jsdom has no AnimationEvent constructor. Presence only reads
// `event.animationName` off the event and requires `event.target === node`,
// so a plain Event with the property defined is enough.
function dispatchAnimationEnd(element: Element, animationName: string) {
	const event = new Event("animationend");
	Object.defineProperty(event, "animationName", { value: animationName });
	element.dispatchEvent(event);
}

// The spawning menu's teardown has fully completed: content unmounted (after
// its exit animation in a real renderer), and Radix's deferred FocusScope
// restore — dispatched one macrotask after the unmount — has run.
async function waitForMenuTeardown() {
	await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
	await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("SessionActionsMenu > Switch agent", () => {
	it("keeps the switch-agent dialog open after choosing it from the actions menu", async () => {
		renderHarness();

		await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Switch agent" }));

		// The bug: the dialog opens and is immediately dismissed by Radix's
		// DismissableLayer reacting to the same click that closed the dropdown.
		// Assert it is still there after the dropdown's close animation/focus
		// return has had a chance to run.
		const dialog = await screen.findByRole("dialog", { name: "Switch agent" });
		await waitFor(() => expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument());
		expect(dialog).toBeInTheDocument();
	});

	// The reviewer's exact real-renderer timeline: Radix Presence retains the
	// closing menu for the whole `animate-popover-out` exit (100ms), and only
	// when that animation ends does the content unmount and its FocusScope run
	// the deferred focus restore. The dismissal suppression must survive that
	// whole sequence — a guard expiring early lets the late restore re-dismiss
	// the dialog, and jsdom's default "no animations" behavior hides the bug.
	//
	// While the retained menu is still mounted, its `hideOthers` a11y lock
	// (undone only on unmount) marks everything outside it aria-hidden — the
	// dialog included. That is real-renderer behavior too, so the pre-teardown
	// queries use hidden:true to see through it; after the teardown they are
	// back in the accessible tree.
	it("keeps the dialog open through the menu's retained exit animation and focus restore", async () => {
		const restoreStyles = fakeMenuExitAnimation();
		try {
			renderHarness();

			await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
			await userEvent.click(await screen.findByRole("menuitem", { name: "Switch agent" }));
			const dialog = await screen.findByRole("dialog", { name: "Switch agent", hidden: true });

			// Presence is holding the content for the animate-popover-out exit...
			const menu = screen.getByRole("menu");
			expect(menu.getAttribute("data-state")).toBe("closed");
			// ...and focus has already moved into the dialog, not back to the trigger.
			expect(dialog.contains(document.activeElement)).toBe(true);
			expect(document.activeElement).not.toBe(screen.getByRole("button", { name: "Session actions", hidden: true }));

			// Only now does the menu unmount and run the deferred FocusScope
			// restore that used to re-dismiss the dialog.
			dispatchAnimationEnd(menu, "animate-popover-out");
			await waitForMenuTeardown();

			// Post-animation state: the dialog survived the genuine teardown.
			expect(screen.getByRole("dialog", { name: "Switch agent" })).toBe(dialog);
			expect(dialog.contains(document.activeElement)).toBe(true);
			expect(document.activeElement).not.toBe(screen.getByRole("button", { name: "Session actions" }));
		} finally {
			restoreStyles();
		}
	});

	it("still closes the dialog on a genuine outside click", async () => {
		renderHarness();

		await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Switch agent" }));
		await screen.findByRole("dialog", { name: "Switch agent" });

		// Let the opening menu's teardown settle, then click somewhere genuinely
		// outside the dialog — this must still dismiss it.
		await waitForMenuTeardown();
		await userEvent.click(document.body);

		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	// The opening-race exemption is scoped to the opening interaction only.
	// Once the dismissed menu's teardown has settled, the session actions
	// trigger is a genuine outside element again: clicking it (to reopen the
	// menu) must dismiss the dialog instead of being swallowed.
	it("dismisses the dialog when the actions-menu trigger is clicked again later", async () => {
		renderHarness();

		await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Switch agent" }));
		await screen.findByRole("dialog", { name: "Switch agent" });

		// Let the opening menu's teardown close the exemption before the later
		// interaction — and prove the dialog survived that settle, so a
		// prematurely-disarmed guard cannot hide behind the dismissal below.
		await waitForMenuTeardown();
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Session actions" }));

		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	// Keyboard focus: once the opening interaction has settled, moving focus
	// to an element outside the non-modal dialog is a genuine outside focus
	// interaction and must dismiss the dialog rather than be suppressed.
	it("dismisses the dialog when focus moves outside it later", async () => {
		renderHarness();

		await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Switch agent" }));
		await screen.findByRole("dialog", { name: "Switch agent" });

		await waitForMenuTeardown();
		expect(screen.getByRole("dialog", { name: "Switch agent" })).toBeInTheDocument();

		// Radix returns focus to the trigger when the menu closes, so blur first
		// to make the refocus an actual focus change (a fresh focusin outside
		// the dialog's layer).
		const trigger = screen.getByRole("button", { name: "Session actions" });
		trigger.blur();
		trigger.focus();

		await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	});

	// Suppressing the opening-race dismissal must not leave keyboard focus
	// stranded on the external session-actions trigger: once the opening
	// menu's teardown has fully settled, focus has to live inside the opened
	// dialog.
	it("moves focus into the opened dialog", async () => {
		renderHarness();

		await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Switch agent" }));
		const dialog = await screen.findByRole("dialog", { name: "Switch agent" });

		// Let the opening focus race (dialog FocusScope vs. the closing menu's
		// deferred focus restore) settle through the menu's full teardown.
		await waitForMenuTeardown();

		await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement | null));
		expect(document.activeElement).not.toBe(screen.getByRole("button", { name: "Session actions" }));
	});
});
