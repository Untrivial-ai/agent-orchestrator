import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useRef } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { shellTerminalsQueryKey, type ShellTerminal } from "../hooks/useShellTerminals";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import type { AttachableTerminal } from "../hooks/useTerminalSession";
import type { TerminalTarget } from "../types/terminal";
import type { WorkspaceSession } from "../types/workspace";
import { useUiStore } from "../stores/ui-store";
import {
	TerminalCacheProvider,
	TerminalPane,
	providerScrollsByKeyboard,
} from "./TerminalPane";
import { TooltipProvider } from "./ui/tooltip";

const {
	attachMock,
	getMock,
	postMock,
	prepareForActivationMock,
	sendUserInputMock,
	terminalError,
	terminalState,
	replaySettled,
	hasAttached,
	terminalSessionOptions,
	xtermMounts,
	xtermUnmounts,
	xtermFocusRequests,
} = vi.hoisted(
	() => ({
		attachMock: vi.fn(() => vi.fn()),
		getMock: vi.fn(async (_path: string, _options: unknown) => ({ data: undefined })),
		postMock: vi.fn(),
		prepareForActivationMock: vi.fn(async (): Promise<void> => undefined),
		sendUserInputMock: vi.fn(),
		terminalError: { value: undefined as string | undefined },
		terminalState: { value: "idle" },
		replaySettled: { value: true },
		hasAttached: { value: false },
		terminalSessionOptions: [] as Array<{ coverInitialReplay?: boolean; shellTerminalHandleId?: string }>,
		xtermMounts: { value: 0 },
		xtermUnmounts: { value: 0 },
		xtermFocusRequests: { value: 0 },
	}),
);
let terminalLinkHandler: ((uri: string) => void) | undefined;

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (
			path: string,
			options: { params?: { path?: { sessionId?: string } } },
		) => getMock(path, options),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("./XtermTerminal", () => ({
	XtermTerminal: (props: {
		focusRequested?: boolean;
		isVisible?: boolean;
		onLinkOpen?: (uri: string) => void;
		onReady?: (terminal: AttachableTerminal) => void;
	}) => {
		terminalLinkHandler = props.onLinkOpen;
		const instance = useRef(0);
		if (instance.current === 0) {
			xtermMounts.value += 1;
			instance.current = xtermMounts.value;
		}
		useEffect(() => {
			if (props.focusRequested && props.isVisible !== false) xtermFocusRequests.value += 1;
		}, [props.focusRequested, props.isVisible]);
		useEffect(() => {
			const disposable = { dispose: vi.fn() };
			props.onReady?.({
				cols: 80,
				rows: 24,
				write: vi.fn((_data, done) => done?.()),
				writeln: vi.fn(),
				showLatestOutput: vi.fn(),
				prepareForActivation: prepareForActivationMock,
				notifyCursorColorScheme: vi.fn(),
				sendUserInput: sendUserInputMock,
				onUserInput: vi.fn(() => disposable),
				onResize: vi.fn(() => disposable),
			});
			return () => {
				xtermUnmounts.value += 1;
			};
		}, []);
		return <div data-testid="xterm" data-xterm-instance={instance.current} tabIndex={-1} />;
	},
}));

vi.mock("../hooks/useTerminalSession", () => ({
	useTerminalSession: (
		_session: WorkspaceSession | undefined,
		options: { coverInitialReplay?: boolean; shellTerminalHandleId?: string },
	) => {
		terminalSessionOptions.push(options);
		return {
			attach: attachMock,
			state: terminalState.value,
			error: terminalError.value,
			replaySettled: replaySettled.value,
			hasAttached: hasAttached.value,
		};
	},
}));

const worker = {
	id: "sess-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "claude-code",
	kind: "worker",
	branch: "ao/sess-1",
	status: "working",
	updatedAt: "2026-06-10T00:00:00Z",
	prs: [],
} satisfies WorkspaceSession;

const orchestrator = {
	...worker,
	id: "sess-orch",
	title: "orchestrate",
	kind: "orchestrator",
} satisfies WorkspaceSession;

beforeEach(() => {
	getMock.mockClear();
	postMock.mockReset();
	postMock.mockResolvedValue({ data: {} });
	terminalError.value = undefined;
	terminalState.value = "idle";
	replaySettled.value = true;
	hasAttached.value = false;
	terminalLinkHandler = undefined;
	terminalSessionOptions.length = 0;
	attachMock.mockClear();
	prepareForActivationMock.mockReset();
	prepareForActivationMock.mockResolvedValue(undefined);
	sendUserInputMock.mockReset();
	sendUserInputMock.mockReturnValue(true);
	xtermMounts.value = 0;
	xtermUnmounts.value = 0;
	xtermFocusRequests.value = 0;
	useUiStore.setState({ inspectorSessions: {} });
});

function renderPane(
	session?: WorkspaceSession,
	inputRequest?: { id: number; data: string },
	onInputRequestResult?: (id: number, accepted: boolean) => void,
) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const previousAO = window.ao;
	window.ao = {} as typeof window.ao;
	const result = render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<TerminalPane
					daemonReady
					fontSize={12}
					inputRequest={inputRequest}
					onInputRequestResult={onInputRequestResult}
					session={session}
					theme="dark"
				/>
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return {
		...result,
		queryClient,
		restore: () => {
			window.ao = previousAO;
		},
	};
}

function workspaceWithSessions(sessions: WorkspaceSession[]) {
	return [
		{
			id: "proj-1",
			name: "my-app",
			kind: "single_repo" as const,
			path: "/repo/my-app",
			type: "main" as const,
			sessions,
		},
	];
}

function renderCachedPane({
	session,
	sessions,
	shellTerminals = [],
	terminalTarget,
	focusRequested = false,
}: {
	session?: WorkspaceSession;
	sessions: WorkspaceSession[];
	shellTerminals?: ShellTerminal[];
	terminalTarget?: TerminalTarget;
	focusRequested?: boolean;
}) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	queryClient.setQueryData(workspaceQueryKey, workspaceWithSessions(sessions));
	queryClient.setQueryData(shellTerminalsQueryKey, shellTerminals);
	const previousAO = window.ao;
	window.ao = {} as typeof window.ao;

	const tree = (nextSession?: WorkspaceSession, nextTarget?: TerminalTarget, showPane = true) => (
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<TerminalCacheProvider daemonReady theme="dark">
					{showPane ? (
						<TerminalPane
							daemonReady
							fontSize={12}
							session={nextSession}
							terminalTarget={nextTarget}
							theme="dark"
							focusRequested={focusRequested}
						/>
					) : (
						<div data-testid="away" />
					)}
				</TerminalCacheProvider>
			</TooltipProvider>
		</QueryClientProvider>
	);
	const result = render(tree(session, terminalTarget));
	return {
		...result,
		queryClient,
		show: (nextSession?: WorkspaceSession, nextTarget?: TerminalTarget) =>
			result.rerender(tree(nextSession, nextTarget)),
		restore: () => {
			window.ao = previousAO;
		},
	};
}

function activeXterm(): HTMLElement {
	return within(screen.getByTestId("session-terminal-slot")).getByTestId("xterm");
}

describe("TerminalPane empty states", () => {
	it("reports terminal attachment state changes to an optional observer", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const previousAO = window.ao;
		window.ao = {} as typeof window.ao;
		const onTerminalStateChange = vi.fn();
		const target = {
			kind: "shell" as const,
			handleId: "shellterm-login-1",
			generation: "2026-08-29T12:00:00Z",
			title: "Codex login",
		};
		const view = render(
			<QueryClientProvider client={queryClient}>
				<TerminalPane daemonReady fontSize={12} onTerminalStateChange={onTerminalStateChange} terminalTarget={target} theme="dark" />
			</QueryClientProvider>,
		);
		try {
			await waitFor(() => expect(onTerminalStateChange).toHaveBeenLastCalledWith("idle"));
			terminalState.value = "exited";
			view.rerender(
				<QueryClientProvider client={queryClient}>
					<TerminalPane daemonReady fontSize={12} onTerminalStateChange={onTerminalStateChange} terminalTarget={target} theme="dark" />
				</QueryClientProvider>,
			);
			await waitFor(() => expect(onTerminalStateChange).toHaveBeenLastCalledWith("exited"));
		} finally {
			window.ao = previousAO;
		}
	});

	it("uses the full top, right, and bottom extent for the terminal grid", () => {
		const view = renderPane({ ...worker, terminalHandleId: "term-1" });
		try {
			expect(screen.getByTestId("xterm").parentElement).toHaveClass("pl-2");
			expect(screen.getByTestId("xterm").parentElement).not.toHaveClass("pt-2", "pr-2", "pb-2", "p-2");
		} finally {
			view.restore();
		}
	});

	it("forwards an explicit input request after the terminal is attached", async () => {
		terminalState.value = "attached";
		const onInputRequestResult = vi.fn();
		const view = renderPane(
			{ ...worker, terminalHandleId: "term-1" },
			{ id: 1, data: "/login\r" },
			onInputRequestResult,
		);
		try {
			await waitFor(() => expect(sendUserInputMock).toHaveBeenCalledWith("/login\r"));
			expect(onInputRequestResult).toHaveBeenCalledWith(1, true);
		} finally {
			view.restore();
		}
	});

	it("reports rejected input requests without consuming them", async () => {
		terminalState.value = "attached";
		sendUserInputMock.mockReturnValue(false);
		const onInputRequestResult = vi.fn();
		const view = renderPane(
			{ ...worker, terminalHandleId: "term-1" },
			{ id: 1, data: "/login\r" },
			onInputRequestResult,
		);
		try {
			await waitFor(() => expect(onInputRequestResult).toHaveBeenCalledWith(1, false));
		} finally {
			view.restore();
		}
	});

	it("shows a no-selection message when no session is selected", () => {
		const view = renderPane();
		try {
			expect(screen.getByText("Agent Orchestrator")).toBeInTheDocument();
			expect(screen.getByText("No session selected. Pick a worker to attach its terminal.")).toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("shows a startup message when a selected session has no terminal handle yet", () => {
		const view = renderPane(worker);
		try {
			expect(screen.getByText("Starting session")).toBeInTheDocument();
			expect(
				screen.getByText(
					"Preparing the worker terminal. This can take a moment while AO creates the workspace and starts the agent.",
				),
			).toBeInTheDocument();
			expect(screen.queryByText("No session selected. Pick a worker to attach its terminal.")).not.toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("shows orchestrator-specific startup copy for a pending orchestrator terminal", () => {
		const view = renderPane(orchestrator);
		try {
			expect(screen.getByText("Starting session")).toBeInTheDocument();
			expect(
				screen.getByText(
					"Preparing the orchestrator terminal. This can take a moment while AO creates the workspace and starts the agent.",
				),
			).toBeInTheDocument();
			expect(screen.queryByText(/worker terminal/i)).not.toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("selects a temporary shell without attaching xterm to its temporary handle", () => {
		const shell = {
			handleId: "pending-shell:test",
			sessionId: worker.id,
			workingDir: "",
			title: "Terminal 1",
			createdAt: "2026-08-31T00:00:00Z",
			optimistic: true,
		} satisfies ShellTerminal;
		const view = renderCachedPane({
			session: worker,
			sessions: [worker],
			// The authoritative shell can replace the optimistic cache row before
			// the selected target changes. A pending handle itself is the contract.
			shellTerminals: [],
			terminalTarget: {
				generation: shell.createdAt,
				kind: "shell",
				handleId: shell.handleId,
				sessionId: worker.id,
				title: shell.title,
			},
		});
		try {
			expect(screen.getByTestId("optimistic-terminal")).toBeInTheDocument();
			expect(screen.queryByTestId("xterm")).not.toBeInTheDocument();
			expect(terminalSessionOptions.at(-1)?.shellTerminalHandleId).toBeUndefined();
		} finally {
			view.restore();
		}
	});
});

// Initial-replay cover (issue #3160): xterm stays mounted and ingesting behind
// a blank cover so the pane is revealed already drawn at the tail.
describe("TerminalPane replay cover", () => {
	beforeEach(() => {
		// The cover is scoped to an attachment that is actually expecting a
		// replay, so these cases have to be in a connecting/attached state.
		terminalState.value = "connecting";
	});

	it("covers the terminal while the attachment is still buffering the replay", () => {
		replaySettled.value = false;
		const view = renderPane({ ...worker, terminalHandleId: "term-1" });
		try {
			const cover = screen.getByTestId("terminal-replay-cover");
			expect(cover).toBeInTheDocument();
			expect(cover).toHaveClass("bg-terminal-opaque", "pointer-events-none");
			expect(cover).not.toHaveClass("terminal-surface");
			// xterm keeps rendering underneath — covered, never unmounted, so the
			// grid it measures stays correct.
			expect(screen.getByTestId("xterm")).toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("uncovers once the replay has settled", () => {
		replaySettled.value = true;
		const view = renderPane({ ...worker, terminalHandleId: "term-1" });
		try {
			expect(screen.queryByTestId("terminal-replay-cover")).not.toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("keeps the cover through the first complete replay paint", async () => {
		let finishPaint: (() => void) | undefined;
		prepareForActivationMock.mockImplementation(
			() => new Promise<void>((resolve) => {
				finishPaint = resolve;
			}),
		);
		replaySettled.value = false;
		const view = renderPane({ ...worker, terminalHandleId: "term-1" });
		try {
			await waitFor(() => expect(screen.getByTestId("terminal-replay-cover")).toBeInTheDocument());
			replaySettled.value = true;
			view.rerender(
				<QueryClientProvider client={view.queryClient}>
					<TooltipProvider>
						<TerminalPane
							daemonReady
							fontSize={12}
							session={{ ...worker, terminalHandleId: "term-1" }}
							theme="dark"
						/>
					</TooltipProvider>
				</QueryClientProvider>,
			);
			expect(screen.getByTestId("terminal-replay-cover")).toBeInTheDocument();
			expect(prepareForActivationMock).toHaveBeenCalled();

			await act(async () => finishPaint?.());
			expect(screen.queryByTestId("terminal-replay-cover")).not.toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("attaches and lifts the cover even when activation preparation rejects", async () => {
		prepareForActivationMock.mockRejectedValue(new Error("viewport gone"));
		replaySettled.value = false;
		const view = renderPane({ ...worker, terminalHandleId: "term-1" });
		try {
			await waitFor(() => expect(attachMock).toHaveBeenCalled());
			replaySettled.value = true;
			view.rerender(
				<QueryClientProvider client={view.queryClient}>
					<TerminalPane
						daemonReady
						fontSize={12}
						session={{ ...worker, terminalHandleId: "term-1" }}
						theme="dark"
					/>
				</QueryClientProvider>,
			);
			await waitFor(() =>
				expect(screen.queryByTestId("terminal-replay-cover")).not.toBeInTheDocument(),
			);
		} finally {
			view.restore();
		}
	});

	it("keeps the replay cover silent even when attachment takes longer", () => {
		vi.useFakeTimers();
		try {
			replaySettled.value = false;
			const view = renderPane({ ...worker, terminalHandleId: "term-1" });
			try {
				act(() => vi.advanceTimersByTime(1_000));
				expect(screen.getByTestId("terminal-replay-cover")).toHaveAttribute("aria-hidden", "true");
				expect(screen.queryByText("Loading latest output…")).not.toBeInTheDocument();
			} finally {
				view.restore();
			}
		} finally {
			vi.useRealTimers();
		}
	});

	it("stays out of the way while the pane is visibly reattaching", () => {
		replaySettled.value = false;
		terminalState.value = "reattaching";
		hasAttached.value = true;
		const view = renderPane({ ...worker, terminalHandleId: "term-1" });
		try {
			// An open timeout lifts the cover and the backoff reconnect would pull
			// it straight back down; the banner explains this window better.
			expect(screen.queryByTestId("terminal-replay-cover")).not.toBeInTheDocument();
			expect(screen.getByText("Terminal disconnected — reattaching…")).toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("keeps the startup card, not the blank cover, when there is no terminal handle yet", () => {
		replaySettled.value = false;
		const view = renderPane(worker);
		try {
			expect(screen.getByText("Starting session")).toBeInTheDocument();
			expect(screen.queryByTestId("terminal-replay-cover")).not.toBeInTheDocument();
		} finally {
			view.restore();
		}
	});
});

describe("TerminalCacheProvider", () => {
	const sessionA = { ...worker, id: "sess-a", title: "session A", terminalHandleId: "handle-a" };
	const sessionB = { ...worker, id: "sess-b", title: "session B", terminalHandleId: "handle-b" };

	it("removes externally-created terminal hosts when the shell provider unmounts", async () => {
		const view = renderCachedPane({ session: sessionA, sessions: [sessionA, sessionB] });
		try {
			await waitFor(() => activeXterm());
			view.show(sessionB);
			await waitFor(() => expect(screen.getAllByTestId("xterm")).toHaveLength(2));
			view.unmount();
			expect(document.querySelectorAll("[data-terminal-cache-key]")).toHaveLength(0);
			expect(xtermUnmounts.value).toBe(2);
		} finally {
			view.restore();
		}
	});

	it("reveals a returning retained terminal even when its preparation rejects", async () => {
		const view = renderCachedPane({ session: sessionA, sessions: [sessionA, sessionB] });
		try {
			await waitFor(() => activeXterm());
			view.show(sessionB);
			await waitFor(() => expect(screen.getAllByTestId("xterm")).toHaveLength(2));

			// Returning to a retained terminal runs the cached preparation path. A
			// rejection there must not strand the entry in "preparing", which stays
			// visibility:hidden and reads as a permanently black pane.
			prepareForActivationMock.mockRejectedValue(new Error("viewport gone"));
			view.show(sessionA);
			await waitFor(() => {
				const container = document.querySelector<HTMLElement>(
					`[data-terminal-cache-key^="session:${sessionA.id}:worker|"]`,
				);
				expect(container?.dataset.terminalActivationPhase).toBe("visible");
				expect(container?.style.visibility).not.toBe("hidden");
			});
		} finally {
			view.restore();
		}
	});

	it("retains visited terminals up to the cap and evicts the oldest parked one beyond it", async () => {
		const sessions = Array.from({ length: 7 }, (_, index) => ({
			...worker,
			id: `sess-retained-${index}`,
			title: `session ${index}`,
			terminalHandleId: `handle-retained-${index}`,
		}));
		const view = renderCachedPane({ session: sessions[0], sessions });
		try {
			const oldest = await waitFor(() => activeXterm());
			for (const session of sessions.slice(1, 6)) {
				view.show(session);
				await waitFor(() =>
					expect(
						document.querySelector(`[data-terminal-cache-key^="session:${session.id}:worker|"]`),
					).not.toBeNull(),
				);
			}
			// Six visited sessions fit the cap, so nothing has been evicted yet.
			expect(document.querySelectorAll("[data-terminal-cache-key]")).toHaveLength(6);
			expect(oldest.isConnected).toBe(true);
			expect(xtermUnmounts.value).toBe(0);

			// The seventh evicts the least-recently-activated parked entry, keeping
			// retained xterm buffers, renderer contexts and mux writers bounded.
			view.show(sessions[6]);
			await waitFor(() => expect(xtermUnmounts.value).toBe(1));
			expect(document.querySelectorAll("[data-terminal-cache-key]")).toHaveLength(6);
			expect(
				document.querySelector(`[data-terminal-cache-key^="session:${sessions[0].id}:worker|"]`),
			).toBeNull();

			// Reopening an evicted session builds a fresh terminal over the covered
			// replay path rather than resurrecting the disposed one.
			view.show(sessions[0]);
			await waitFor(() => expect(activeXterm()).not.toBe(oldest));
			expect(xtermMounts.value).toBe(8);
		} finally {
			view.restore();
		}
	});

	it("focuses each retained TUI terminal when switching among TUI sessions", async () => {
		const tuiA = { ...sessionA, mode: "tui" as const };
		const tuiB = { ...sessionB, mode: "tui" as const };
		const view = renderCachedPane({
			focusRequested: true,
			session: tuiA,
			sessions: [tuiA, tuiB],
		});
		try {
			await waitFor(() => expect(xtermFocusRequests.value).toBe(1));

			view.show(tuiB);
			await waitFor(() => expect(xtermFocusRequests.value).toBe(2));

			view.show(tuiA);
			await waitFor(() => expect(xtermFocusRequests.value).toBe(3));
		} finally {
			view.restore();
		}
	});

	it("reveals a cached terminal immediately while its activation preparation is still pending", async () => {
		prepareForActivationMock.mockImplementation(() => new Promise<void>(() => undefined));
		const view = renderCachedPane({ session: sessionA, sessions: [sessionA, sessionB] });
		try {
			const terminalA = await waitFor(() => activeXterm());
			view.show(sessionB);
			await waitFor(() => expect(activeXterm()).not.toBe(terminalA));

			view.show(sessionA);
			const host = document.querySelector<HTMLElement>(`[data-terminal-cache-key^="session:${sessionA.id}:worker|"]`);
			expect(host).toHaveAttribute("data-terminal-activation-phase", "visible");
			expect(host?.style.visibility).not.toBe("hidden");
		} finally {
			view.restore();
		}
	});

	it("disposes an old handle generation instead of reusing its terminal state", async () => {
		const replacement = { ...sessionA, terminalHandleId: "handle-a-generation-2" };
		const view = renderCachedPane({ session: sessionA, sessions: [sessionA] });
		try {
			const oldGeneration = await waitFor(() => activeXterm());
			act(() => {
				view.queryClient.setQueryData(workspaceQueryKey, workspaceWithSessions([replacement]));
			});
			view.show(replacement);

			await waitFor(() => expect(oldGeneration.isConnected).toBe(false));
			expect(activeXterm()).not.toBe(oldGeneration);
			expect(xtermMounts.value).toBe(2);
			await waitFor(() => expect(xtermUnmounts.value).toBe(1));
			expect(attachMock).toHaveBeenCalledTimes(2);
		} finally {
			view.restore();
		}
	});

	it("disposes an exited PTY attachment when the controller generation changes on the same handle", async () => {
		const first = { ...sessionA, terminalGeneration: "launch-1" };
		const replacement = { ...first, terminalGeneration: "launch-2" };
		const view = renderCachedPane({ session: first, sessions: [first] });
		try {
			const oldGeneration = await waitFor(() => activeXterm());
			act(() => {
				view.queryClient.setQueryData(workspaceQueryKey, workspaceWithSessions([replacement]));
			});
			view.show(replacement);

			await waitFor(() => expect(oldGeneration.isConnected).toBe(false));
			expect(activeXterm()).not.toBe(oldGeneration);
			expect(xtermMounts.value).toBe(2);
			expect(attachMock).toHaveBeenCalledTimes(2);
		} finally {
			view.restore();
		}
	});

	it("disposes parked entries when their session is removed", async () => {
		const view = renderCachedPane({ session: sessionA, sessions: [sessionA, sessionB] });
		try {
			const terminalA = await waitFor(() => activeXterm());
			view.show(sessionB);
			await waitFor(() => expect(activeXterm()).not.toBe(terminalA));
			act(() => {
				view.queryClient.setQueryData(workspaceQueryKey, workspaceWithSessions([sessionB]));
			});

			await waitFor(() => expect(terminalA.isConnected).toBe(false));
			await waitFor(() => expect(xtermUnmounts.value).toBe(1));
		} finally {
			view.restore();
		}
	});

	it("disposes a parked shell when the shell lifecycle removes its handle", async () => {
		const shell: ShellTerminal = {
			handleId: "shell-handle",
			sessionId: sessionA.id,
			workingDir: "/repo/my-app",
			title: "scratch",
			createdAt: "2026-07-30T00:00:00Z",
		};
		const shellTarget: TerminalTarget = {
			generation: shell.createdAt,
			kind: "shell",
			handleId: shell.handleId,
			sessionId: sessionA.id,
			title: shell.title,
		};
		const view = renderCachedPane({
			session: sessionA,
			sessions: [sessionA],
			shellTerminals: [shell],
			terminalTarget: shellTarget,
		});
		try {
			const shellXterm = await waitFor(() => activeXterm());
			view.show(sessionA, { kind: "worker" });
			await waitFor(() => expect(activeXterm()).not.toBe(shellXterm));
			act(() => {
				view.queryClient.setQueryData(shellTerminalsQueryKey, []);
			});

			await waitFor(() => expect(shellXterm.isConnected).toBe(false));
			await waitFor(() => expect(xtermUnmounts.value).toBe(1));
		} finally {
			view.restore();
		}
	});

	it("does not retain reviewer terminals in the worker cache", async () => {
		const reviewer = {
			handleId: "stable-reviewer-handle",
			harness: "codex",
			kind: "reviewer",
			sessionId: sessionA.id,
		} satisfies TerminalTarget;
		const view = renderCachedPane({
			session: sessionA,
			sessions: [sessionA],
			terminalTarget: reviewer,
		});
		try {
			const first = await waitFor(() => screen.getByTestId("xterm"));
			expect(document.querySelector("[data-terminal-cache-key*='reviewer']")).toBeNull();
			expect(terminalSessionOptions.at(-1)?.coverInitialReplay).toBe(false);
			view.show(sessionA, { kind: "worker" });
			await waitFor(() => expect(first.isConnected).toBe(false));
		} finally {
			view.restore();
		}
	});

	it("keeps an attachment error inspectable until the pane leaves", async () => {
		terminalState.value = "error";
		terminalError.value = "attach failed";
		const view = renderCachedPane({ session: sessionA, sessions: [sessionA, sessionB] });
		try {
			await waitFor(() => expect(screen.getByText("Terminal error: attach failed")).toBeInTheDocument());
			expect(activeXterm()).toBeInTheDocument();
			expect(xtermUnmounts.value).toBe(0);
			expect(xtermMounts.value).toBe(1);
			terminalState.value = "attached";
			terminalError.value = undefined;
			view.show(sessionB);

			await waitFor(() => activeXterm());
			expect(xtermMounts.value).toBe(2);
		} finally {
			view.restore();
		}
	});
});

describe("terminal restore", () => {
	it.each([
		["exited", undefined],
		["error", "terminal handle missing"],
		["idle", undefined],
	])("posts restore from the terminal-ended strip when mux state is %s", async (state, error) => {
		terminalState.value = state;
		terminalError.value = error;
		const view = renderPane({ ...worker, status: "terminated", terminalHandleId: "term-1" });
		const invalidate = vi.spyOn(view.queryClient, "invalidateQueries").mockResolvedValue(undefined);
		try {
			await userEvent.click(screen.getByRole("button", { name: "Restore session" }));

			await waitFor(() =>
				expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/restore", {
					params: { path: { sessionId: "sess-1" } },
				}),
			);
			expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
		} finally {
			view.restore();
		}
	});

	it("offers restore when a merged session is terminated", () => {
		const view = renderPane({
			...worker,
			status: "merged",
			isTerminated: true,
			terminalHandleId: "term-1",
		});
		try {
			expect(screen.getByRole("button", { name: "Restore session" })).toBeInTheDocument();
		} finally {
			view.restore();
		}
	});

	it("preserves Restore controls after a cached attachment reports a fatal pane error", async () => {
		terminalState.value = "error";
		terminalError.value = "terminal handle missing";
		const terminated = {
			...worker,
			status: "terminated",
			terminalHandleId: "term-1",
		} satisfies WorkspaceSession;
		const view = renderCachedPane({ session: terminated, sessions: [terminated] });
		try {
			expect(await screen.findByRole("button", { name: "Restore session" })).toBeInTheDocument();
			expect(screen.getByTestId("xterm")).toBeInTheDocument();
			expect(screen.getByText("Terminal error: terminal handle missing")).toBeInTheDocument();
		} finally {
			view.restore();
		}
	});
});

describe("providerScrollsByKeyboard", () => {
	// opencode, its fork kilocode, and grok use TUIs that scroll their own transcripts
	// by keyboard and ignore SGR wheel reports, so they must opt into the
	// PageUp/PageDown wheel routing (see XtermTerminal's paneScrollsByKeyboard).
	it("is true for keyboard-scroll TUIs", () => {
		expect(providerScrollsByKeyboard("opencode")).toBe(true);
		expect(providerScrollsByKeyboard("kilocode")).toBe(true);
		expect(providerScrollsByKeyboard("grok")).toBe(true);
	});

	it("is false for mouse-report/native-scroll providers", () => {
		expect(providerScrollsByKeyboard("codex")).toBe(false);
		expect(providerScrollsByKeyboard("claude-code")).toBe(false);
		// Muse writes its transcript to the normal buffer. PageUp is ignored by
		// Muse, so wheel input must stay on the SGR -> tmux copy-mode path.
		expect(providerScrollsByKeyboard("muse")).toBe(false);
	});

	it("is false when the provider is unknown", () => {
		expect(providerScrollsByKeyboard(undefined)).toBe(false);
	});
});

describe("terminal link preview", () => {
	it.each(["http://localhost:3000/simple", "https://app.localhost:5173", "http://127.0.0.1:8080", "http://[::1]:4173"])(
		"mirrors worker loopback link %s into the session Browser preview",
		async (url) => {
			const view = renderPane(worker);
			try {
				expect(terminalLinkHandler).toBeTypeOf("function");
				act(() => terminalLinkHandler?.(url));

				await waitFor(() =>
					expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/preview", {
						params: { path: { sessionId: "sess-1" } },
						body: { url },
					}),
				);
			} finally {
				view.restore();
			}
		},
	);

	it("mirrors an external (non-loopback) terminal link into the Browser preview", async () => {
		const view = renderPane(worker);
		try {
			act(() => terminalLinkHandler?.("https://example.com/pull/42"));
			expect(useUiStore.getState().inspectorSessions[worker.id]).toMatchObject({
				isOpen: true,
				view: "browser",
			});
			await waitFor(() =>
				expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/preview", {
					params: { path: { sessionId: "sess-1" } },
					body: { url: "https://example.com/pull/42" },
				}),
			);
		} finally {
			view.restore();
		}
	});

	it("does not mirror a non-web (mailto:) link", () => {
		const view = renderPane(worker);
		try {
			act(() => terminalLinkHandler?.("mailto:dev@example.com"));
			expect(postMock).not.toHaveBeenCalled();
		} finally {
			view.restore();
		}
	});

	it("does not POST without a selected session", () => {
		const view = renderPane();
		try {
			act(() => terminalLinkHandler?.("http://localhost:3000"));
			expect(postMock).not.toHaveBeenCalled();
		} finally {
			view.restore();
		}
	});

	it("does not mirror orchestrator links because orchestrators have no Browser inspector", () => {
		const view = renderPane(orchestrator);
		try {
			act(() => terminalLinkHandler?.("http://localhost:3000"));
			expect(postMock).not.toHaveBeenCalled();
		} finally {
			view.restore();
		}
	});

	it("does not mirror links for terminated workers because their Browser inspector is cleared", () => {
		const view = renderPane({ ...worker, status: "terminated" });
		try {
			act(() => terminalLinkHandler?.("http://localhost:3000"));
			expect(postMock).not.toHaveBeenCalled();
		} finally {
			view.restore();
		}
	});

	it("does not mirror links for merged workers whose session is terminated", () => {
		const view = renderPane({ ...worker, status: "merged", isTerminated: true });
		try {
			act(() => terminalLinkHandler?.("http://localhost:3000"));
			expect(postMock).not.toHaveBeenCalled();
		} finally {
			view.restore();
		}
	});

	it("does not invalidate workspace data when the preview endpoint returns an error", async () => {
		postMock.mockResolvedValueOnce({ error: { code: "INVALID_PREVIEW_URL" } });
		const warning = vi.spyOn(console, "warn").mockImplementation(() => undefined);
		const view = renderPane(worker);
		const invalidate = vi.spyOn(view.queryClient, "invalidateQueries");
		try {
			act(() => terminalLinkHandler?.("http://localhost:3000"));
			await waitFor(() => expect(warning).toHaveBeenCalled());
			expect(invalidate).not.toHaveBeenCalled();
		} finally {
			warning.mockRestore();
			view.restore();
		}
	});

	it("handles a rejected preview request without an unhandled rejection", async () => {
		const error = new Error("daemon unavailable");
		postMock.mockRejectedValueOnce(error);
		const warning = vi.spyOn(console, "warn").mockImplementation(() => undefined);
		const view = renderPane(worker);
		const invalidate = vi.spyOn(view.queryClient, "invalidateQueries");
		try {
			act(() => terminalLinkHandler?.("http://localhost:3000"));
			await waitFor(() =>
				expect(warning).toHaveBeenCalledWith("Unable to open link in Browser preview", error),
			);
			expect(invalidate).not.toHaveBeenCalled();
		} finally {
			warning.mockRestore();
			view.restore();
		}
	});
});
