import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { McpServerBanner, ReauthBanner, ThreadStateBanner } from "./ChatStatusBanners";

// Each of these answers a question the timeline structurally cannot, so the tests are
// about what is said and when it is withheld — a banner for an ordinary state is noise
// that teaches readers to ignore the row.

describe("ReauthBanner", () => {
	it.each(["Unauthorized (401)", "Authentication failed", "OAuth token has been revoked"])(
		"offers sign-in based on account state for %s",
		(reason) => {
			render(<ReauthBanner account={{ reauthRequiredAt: "2026-09-14T00:00:00Z", reauthReason: reason }} harness="claude-code" />);
			expect(screen.getByRole("alert")).toHaveTextContent(reason);
			expect(screen.getByText("claude auth login")).toBeInTheDocument();
		},
	);
	it("names the command, because re-authenticating is not something AO can do", () => {
		render(
			<ReauthBanner
				account={{
					reauthRequiredAt: "2026-08-03T00:00:00Z",
					reauthReason: "The stored session expired.",
				}}
				harness="codex"
			/>,
		);
		expect(screen.getByRole("alert")).toBeInTheDocument();
		expect(screen.getByText("codex login")).toBeInTheDocument();
		expect(screen.getByText(/The stored session expired/)).toBeInTheDocument();
	});

	it("says the worktree is untouched, since nothing else about the session works", () => {
		render(
			<ReauthBanner account={{ reauthRequiredAt: "2026-08-03T00:00:00Z" }} harness="codex" />,
		);
		expect(screen.getByText(/worktree is untouched/i)).toBeInTheDocument();
	});

	it("names Claude Code's non-interactive authentication command", () => {
		render(
			<ReauthBanner account={{ reauthRequiredAt: "2026-08-03T00:00:00Z" }} harness="claude-code" />,
		);
		expect(screen.getByText("claude auth login")).toBeInTheDocument();
	});

	it("falls back to generic wording rather than guessing a command", () => {
		render(
			<ReauthBanner account={{ reauthRequiredAt: "2026-08-03T00:00:00Z" }} harness="opencode" />,
		);
		expect(screen.queryByText(/login$/)).not.toBeInTheDocument();
		expect(screen.getByText(/agent’s own CLI/)).toBeInTheDocument();
	});

	it("stays silent for an account with no credential demand", () => {
		const { container } = render(
			<ReauthBanner account={{ authMode: "chatgpt", planLabel: "Pro" }} harness="codex" />,
		);
		expect(container).toBeEmptyDOMElement();
	});
});

describe("ThreadStateBanner", () => {
	it("reports a provider-side fault as the provider's, not AO's connection", () => {
		render(<ThreadStateBanner threadState={{ status: "system_error" }} />);
		expect(screen.getByText(/thread hit an internal error/i)).toBeInTheDocument();
		expect(screen.getByText(/not in AO's connection to it/)).toBeInTheDocument();
	});

	it("reports a closed thread as history AO kept and the agent did not", () => {
		render(<ThreadStateBanner threadState={{ status: "closed" }} />);
		expect(screen.getByText(/closed this thread/i)).toBeInTheDocument();
	});

	it("lists what the provider says it is waiting on", () => {
		render(
			<ThreadStateBanner threadState={{ status: "system_error", waitingOn: ["user_input"] }} />,
		);
		expect(screen.getByText(/Waiting on: user_input/)).toBeInTheDocument();
	});

	// active, idle and not_loaded are the ordinary run of a session.
	it.each(["active", "idle", "not_loaded"] as const)("says nothing for %s", (status) => {
		const { container } = render(<ThreadStateBanner threadState={{ status }} />);
		expect(container).toBeEmptyDOMElement();
	});
});

describe("McpServerBanner", () => {
	afterEach(() => vi.useRealTimers());

	const broken = [
		{
			name: "playwright",
			status: "failed" as const,
			failureReason: "startup_timeout",
			error: "did not report ready within 30s",
		},
	];

	it("shows a compact, non-actionable notice for three and a half seconds", async () => {
		vi.useFakeTimers();
		render(<McpServerBanner servers={broken} />);

		expect(screen.getByRole("status")).toHaveTextContent("Playwright MCP unavailable");
		expect(screen.getByRole("status")).not.toHaveTextContent("1 tool server unavailable");
		expect(screen.queryByText(/startup_timeout/)).not.toBeInTheDocument();
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
		expect(screen.getByRole("status").parentElement).toHaveClass(
			"absolute",
			"bottom-full",
			"w-fit",
			"origin-center",
		);

		act(() => vi.advanceTimersByTime(3_500));
		vi.useRealTimers();
		await new Promise((resolve) => window.setTimeout(resolve, 250));
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("lists affected server names as titled MCPs", () => {
		render(<McpServerBanner servers={[...broken, { name: "notion", status: "failed" }]} />);
		expect(screen.getByRole("status")).toHaveTextContent("Playwright, Notion MCPs unavailable");
	});

	// A healthy server is not news. The caller filters, and an empty list must not
	// leave a permanent bar above the conversation saying nothing is wrong.
	it("says nothing when no server is broken", () => {
		const { container } = render(<McpServerBanner servers={[]} />);
		expect(container).toBeEmptyDOMElement();
	});
});
