import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	createCloudPendingSession,
	registerCloudPendingSession,
	resetCloudPendingSessionsForTests,
	useCloudPendingSession,
} from "../lib/cloud-pending-session";
import { CloudPendingSession } from "./CloudPendingSession";

const mocks = vi.hoisted(() => ({
	progress: { phase: "preparing_repository" as const },
	resumeSession: vi.fn(),
}));

vi.mock("../lib/cloud-startup-progress", () => ({
	useCloudStartupProgress: () => mocks.progress,
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({ client: { resumeSession: mocks.resumeSession } }),
}));

function register(options?: {
	attemptId?: string;
	create?: (key: string) => Promise<string>;
	send?: (sessionId: string, message: { clientSequence: number; text: string }, key: string) => Promise<void>;
	startedAtMs?: number;
}) {
	const attemptId = options?.attemptId ?? "attempt-1";
	return registerCloudPendingSession({
		attempt: { attemptId, startedAtMs: options?.startedAtMs ?? performance.now() },
		create: options?.create ?? (async () => "session-1"),
		initialPrompt: "Build the requested feature",
		orgId: "org-1",
		projectId: "project-1",
		send: options?.send ?? (async () => undefined),
	});
}

function PendingHarness({ identifier }: { identifier: string }) {
	const attempt = useCloudPendingSession(identifier);
	return attempt ? <CloudPendingSession attempt={attempt} /> : null;
}

afterEach(() => {
	resetCloudPendingSessionsForTests();
	mocks.resumeSession.mockReset();
	mocks.progress = { phase: "preparing_repository" };
});

describe("CloudPendingSession", () => {
	it("accepts input immediately and flushes it after session creation", async () => {
		const user = userEvent.setup();
		const send = vi.fn(async () => undefined);
		const pending = register({ send });
		render(<PendingHarness identifier={pending.routeSessionId} />);

		const composer = screen.getByRole("textbox", { name: "Add another instruction" });
		await waitFor(() => expect(composer).toHaveFocus());
		await user.type(composer, "Run the focused tests{Enter}");

		const messageRow = screen.getByTestId("pending-message");
		expect(within(messageRow).getByText("Run the focused tests")).toBeInTheDocument();
		expect(within(messageRow).getByText("Saving")).toBeInTheDocument();
		expect(composer).toHaveFocus();

		await act(async () => {
			await createCloudPendingSession(pending.attemptId);
		});
		await waitFor(() => expect(send).toHaveBeenCalledOnce());
		expect(send).toHaveBeenCalledWith(
			"session-1",
			{ clientSequence: 1, text: "Run the focused tests" },
			expect.any(String),
		);
		expect(within(messageRow).getByText("Queued")).toBeInTheDocument();
	});

	it("keeps the same focused composer and draft when the route binds to the durable id", async () => {
		const user = userEvent.setup();
		const pending = register();
		const view = render(<PendingHarness identifier={pending.routeSessionId} />);
		const composer = screen.getByRole("textbox", { name: "Add another instruction" });
		await user.type(composer, "unfinished draft");

		await act(async () => {
			await createCloudPendingSession(pending.attemptId);
		});
		view.rerender(<PendingHarness identifier="session-1" />);

		expect(screen.getByRole("textbox", { name: "Add another instruction" })).toBe(composer);
		expect(composer).toHaveValue("unfinished draft");
		expect(composer).toHaveFocus();
	});

	it("shows the long-wait state and retries a durable startup without losing the draft", async () => {
		const user = userEvent.setup();
		mocks.resumeSession.mockResolvedValue({});
		const pending = register({ startedAtMs: performance.now() - 91_000 });
		await createCloudPendingSession(pending.attemptId);
		render(<PendingHarness identifier={pending.routeSessionId} />);

		const composer = screen.getByRole("textbox", { name: "Add another instruction" });
		await user.type(composer, "keep this text");
		expect(screen.getByText("Taking longer than usual")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Retry startup" }));

		await waitFor(() => expect(mocks.resumeSession).toHaveBeenCalledWith("org-1", "session-1"));
		expect(composer).toHaveValue("keep this text");
		expect(composer).toHaveFocus();
	});

	it("retains the initial task and exposes create failure retry", async () => {
		const user = userEvent.setup();
		const create = vi.fn()
			.mockRejectedValueOnce(new Error("capacity unavailable"))
			.mockResolvedValueOnce("session-1");
		const pending = register({ create });
		await createCloudPendingSession(pending.attemptId);
		render(<PendingHarness identifier={pending.routeSessionId} />);

		expect(screen.getByText("Build the requested feature")).toBeInTheDocument();
		expect(screen.getAllByText("capacity unavailable")).toHaveLength(2);
		await user.click(screen.getByRole("button", { name: "Retry startup" }));

		await waitFor(() => expect(create).toHaveBeenCalledTimes(2));
		expect(create.mock.calls[1]?.[0]).toBe(create.mock.calls[0]?.[0]);
	});

	it("hides elapsed startup copy after session creation fails", async () => {
		const pending = register({
			create: async () => {
				throw new Error("route unavailable");
			},
			startedAtMs: performance.now() - 21_000,
		});
		await createCloudPendingSession(pending.attemptId);
		render(<PendingHarness identifier={pending.routeSessionId} />);

		expect(screen.getByText("Saving session failed")).toBeInTheDocument();
		expect(screen.queryByText("Still working")).not.toBeInTheDocument();
		expect(screen.queryByText("Taking longer than usual")).not.toBeInTheDocument();
	});
});
