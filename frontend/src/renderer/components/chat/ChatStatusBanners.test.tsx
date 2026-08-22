import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { McpServerBanner, ReauthBanner, ThreadStateBanner } from "./ChatStatusBanners";

// Each of these answers a question the timeline structurally cannot, so the tests are
// about what is said and when it is withheld — a banner for an ordinary state is noise
// that teaches readers to ignore the row.

describe("ReauthBanner", () => {
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
	it("reports transient connection loss without condemning the thread", () => {
		render(
			<ThreadStateBanner
				threadState={{ status: "idle", connectionLostAt: "2026-08-20T22:24:33Z" }}
			/>,
		);
		expect(screen.getByText(/connection lost during this turn/i)).toBeInTheDocument();
		expect(screen.getByText(/thread is still healthy/i)).toBeInTheDocument();
		expect(screen.getByText(/send your message again to retry/i)).toBeInTheDocument();
		expect(screen.queryByText(/new turns will usually fail/i)).not.toBeInTheDocument();
	});

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
	const broken = [
		{
			name: "playwright",
			status: "failed" as const,
			failureReason: "startup_timeout",
			error: "did not report ready within 30s",
		},
	];

	it("says the agent will work around the missing tools silently", () => {
		render(<McpServerBanner servers={broken} />);
		expect(screen.getByText("A tool server did not start")).toBeInTheDocument();
		expect(screen.getByText(/works around them\s+silently/)).toBeInTheDocument();
	});

	it("names the server, its classification and the provider's own text", () => {
		render(<McpServerBanner servers={broken} />);
		expect(screen.getByText("playwright")).toBeInTheDocument();
		expect(screen.getByText(/startup_timeout/)).toBeInTheDocument();
		expect(screen.getByText(/did not report ready within 30s/)).toBeInTheDocument();
	});

	it("offers a reload", async () => {
		const onReload = vi.fn();
		render(<McpServerBanner servers={broken} onReload={onReload} />);
		await userEvent.click(screen.getByRole("button", { name: /Reload/ }));
		expect(onReload).toHaveBeenCalledOnce();
	});

	// The daemon refuses a reload mid-turn, so the control explains itself rather than
	// being allowed to fail.
	it("disables the reload mid-turn and says why", () => {
		render(<McpServerBanner servers={broken} onReload={vi.fn()} turnInFlight />);
		const button = screen.getByRole("button", { name: /Reload/ });
		expect(button).toBeDisabled();
		expect(button).toHaveAttribute(
			"title",
			expect.stringContaining("Finish or stop the current turn"),
		);
	});

	it("draws no control at all when the harness cannot reload", () => {
		render(<McpServerBanner servers={broken} />);
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});

	it("surfaces a failed reload", () => {
		render(<McpServerBanner servers={broken} onReload={vi.fn()} error="controller not ready" />);
		expect(screen.getByText("controller not ready")).toBeInTheDocument();
	});

	// A healthy server is not news. The caller filters, and an empty list must not
	// leave a permanent bar above the conversation saying nothing is wrong.
	it("says nothing when no server is broken", () => {
		const { container } = render(<McpServerBanner servers={[]} />);
		expect(container).toBeEmptyDOMElement();
	});
});
