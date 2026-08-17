import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ActivityRow, SteerMessage } from "./ChatTimelineItems";
import type { ConversationActivity } from "../../types/conversation";

// One test file per new provider signal the daemon started serving. What each covers
// is the claim the row makes, not its markup: an MCP call must not read as a command
// that ran in the worktree, an auto-review must not read as something the user
// approved, and a reasoning row must not be blank when the provider sent prose.

function activity(overrides: Partial<ConversationActivity>): ConversationActivity {
	return {
		kind: "activity",
		id: "act-1",
		sequence: 1,
		revision: 0,
		activityKind: "command",
		status: "completed",
		summary: "summary",
		createdAt: new Date().toISOString(),
		...overrides,
	};
}

describe("reasoning", () => {
	it("renders the streamed summary as prose", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "reasoning",
					summary: "Reasoning",
					// The field the provider actually fills. An earlier build read one that
					// does not exist, which is why these rows arrived blank.
					detail: { text: "**Reading the worktree**\n\nStatus first, then the diff." },
				})}
			/>,
		);
		expect(screen.getByText("Reading the worktree")).toBeInTheDocument();
		expect(screen.getByText(/Status first, then the diff/)).toBeInTheDocument();
	});

	it("says it is still thinking while the summary streams", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "reasoning",
					status: "running",
					detail: { text: "Weighing two approaches" },
				})}
			/>,
		);
		expect(screen.getByText("Thinking")).toBeInTheDocument();
		expect(screen.getByLabelText("still thinking")).toBeInTheDocument();
	});

	it("says so when the summary was cut at the storage cap", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "reasoning",
					detail: { text: "A long chain of thought", textTruncated: true },
				})}
			/>,
		);
		expect(screen.getByText(/longer than AO stores/i)).toBeInTheDocument();
	});

	// The default install sends the item with no body. A labelled empty row teaches
	// nothing, so the surface explains the emptiness once instead.
	it("renders nothing for a reasoning item with no body", () => {
		const { container } = render(
			<ActivityRow activity={activity({ activityKind: "reasoning", detail: {} })} />,
		);
		expect(container).toBeEmptyDOMElement();
	});
});

describe("MCP tool call", () => {
	const call = activity({
		activityKind: "mcp_tool",
		summary: "search_issues",
		detail: {
			server: "github",
			toolName: "search_issues",
			arguments: { repo: "aoagents/ao", state: "open" },
			result: { total: 2 },
			success: true,
		},
	});

	it("names the server and the tool, not a shell command", () => {
		render(<ActivityRow activity={call} />);
		expect(screen.getByText("github")).toBeInTheDocument();
		expect(screen.getByText("search_issues")).toBeInTheDocument();
		// The distinction the kind exists to draw: nothing ran in the worktree.
		expect(screen.getByText("MCP tool")).toBeInTheDocument();
	});

	it("shows the arguments and the answer when opened", async () => {
		render(<ActivityRow activity={call} />);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("Arguments")).toBeInTheDocument();
		expect(screen.getByText("Result")).toBeInTheDocument();
		expect(screen.getByText(/aoagents\/ao/)).toBeInTheDocument();
	});

	it("reports a failed call as failed", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "mcp_tool",
					status: "failed",
					detail: { server: "github", toolName: "search_issues", error: "rate limited" },
				})}
			/>,
		);
		expect(screen.getByText("failed")).toBeInTheDocument();
	});

	it("surfaces the tool's error text", async () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "mcp_tool",
					status: "failed",
					detail: { toolName: "query", error: "connection refused" },
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("connection refused")).toBeInTheDocument();
	});

	// A payload the daemon could not store arrives as a stand-in. Printing it as the
	// tool's answer would be a small lie with a confusing shape.
	it("says a payload was too large rather than printing the stand-in", async () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "mcp_tool",
					detail: {
						toolName: "dump",
						result: { truncated: true, bytes: 2_097_152, note: "exceeded the cap" },
					},
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText(/larger than AO stores/i)).toBeInTheDocument();
		expect(screen.getByText(/2\.0 MB/)).toBeInTheDocument();
	});

	it("shows the latest progress line while a long call runs", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "mcp_tool",
					status: "running",
					detail: { toolName: "crawl", progress: "page 1\npage 2\npage 3" },
				})}
			/>,
		);
		expect(screen.getByText("page 3")).toBeInTheDocument();
		expect(screen.getByLabelText("running")).toBeInTheDocument();
	});
});

describe("auto review", () => {
	const review = activity({
		activityKind: "auto_review",
		summary: "curl -s https://proxy.golang.org/…",
		detail: {
			reviewId: "rev-1",
			actionType: "command",
			status: "approved",
			command: "curl -s https://proxy.golang.org/",
			riskLevel: "low",
			rationale: "Read-only request to an allow-listed host.",
			decisionSource: "agent",
			durationMs: 812,
		},
	});

	it("reads as a decision taken for the user, not one they made", async () => {
		render(<ActivityRow activity={review} />);
		expect(screen.getByText("Auto-approved")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText(/You were not asked/)).toBeInTheDocument();
	});

	it("makes the rationale reachable", async () => {
		render(<ActivityRow activity={review} />);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("Read-only request to an allow-listed host.")).toBeInTheDocument();
	});

	it("carries the provider's own word for who decided", async () => {
		render(<ActivityRow activity={review} />);
		await userEvent.click(screen.getByRole("button"));
		// "agent" means a model made the call, which is materially different from a
		// policy rule matching — so it is not flattened to "automatically".
		expect(screen.getByText("agent · 812ms")).toBeInTheDocument();
	});

	it("shows the risk the provider assessed", () => {
		render(<ActivityRow activity={review} />);
		expect(screen.getByTitle("Risk assessed as low")).toBeInTheDocument();
	});

	it("calls a denial a denial", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "auto_review",
					summary: "rm -rf /",
					detail: { status: "denied", rationale: "Destructive and outside the worktree." },
				})}
			/>,
		);
		expect(screen.getByText("Auto-declined")).toBeInTheDocument();
	});

	// An auto-review's `files` are bare paths, while a file_change's are objects. Same
	// key on the wire, different shape, discriminated only by the kind.
	it("lists the paths an action was allowed to touch", async () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "auto_review",
					summary: "changes to 2 file(s)",
					detail: { status: "approved", files: ["src/a.ts", "src/b.ts"] },
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("src/a.ts, src/b.ts")).toBeInTheDocument();
	});
});

describe("system events", () => {
	it("names what actually answered when the provider rerouted", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "system",
					summary: "Model rerouted",
					detail: {
						event: "model.rerouted",
						fromModel: "gpt-5.6-terra",
						toModel: "gpt-5.6-terra-mini",
						reason: "at capacity",
					},
				})}
			/>,
		);
		expect(screen.getByText("gpt-5.6-terra-mini")).toBeInTheDocument();
		expect(screen.getByText("gpt-5.6-terra")).toBeInTheDocument();
		expect(screen.getByText("at capacity")).toBeInTheDocument();
	});

	it("records where the provider demanded credentials", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "system",
					status: "failed",
					summary: "The provider needs you to sign in again",
					detail: { event: "auth.reauth_required", reason: "refresh token rejected" },
				})}
			/>,
		);
		expect(screen.getByText(/asked you to sign in again/i)).toBeInTheDocument();
		expect(screen.getByText("refresh token rejected")).toBeInTheDocument();
	});
});

describe("steer message", () => {
	it("shows the user's own words and says they landed mid-turn", () => {
		render(
			<SteerMessage
				activity={activity({
					activityKind: "system",
					summary: "Skip the integration tests",
					detail: { event: "steer", text: "Skip the integration tests", origin: "human" },
				})}
			/>,
		);
		expect(screen.getByText("Skip the integration tests")).toBeInTheDocument();
		expect(screen.getByText(/Steered into the running turn/i)).toBeInTheDocument();
	});
});

describe("provider error", () => {
	const envelope = JSON.stringify({
		error: {
			message: "Reconnecting... [1/5] ",
			codexErrorInfo: { responseStreamDisconnected: { httpStatusCode: null } },
			additionalDetails:
				"stream disconnected before completion: You have no credits remaining. Add credits to continue using the API at https://platform.openai.com/settings/organization/billing",
		},
		threadId: "019feb6c-42f9-7411-b296-fc694ae7c69e",
		turnId: "019ffd9d-714c-7d31-932f-4e7c10cf5a82",
		willRetry: true,
	});
	// What AO persists: the Codex envelope, prefixed, in summary and detail.error.
	const stored = `provider error: ${envelope.length > 400 ? `${envelope.slice(0, 400)}…` : envelope}`;

	it("unwraps the truncated prefixed payload AO actually stores", () => {
		expect(envelope.length).toBeGreaterThan(400);
		render(
			<ActivityRow
				activity={activity({
					activityKind: "error",
					status: "failed",
					summary: stored,
					detail: { error: stored },
				})}
			/>,
		);
		expect(screen.getByText("Reconnecting... [1/5]")).toBeInTheDocument();
		expect(screen.getByText(/You have no credits remaining/i)).toBeInTheDocument();
		// The raw envelope must not paint as the row label — that is what overflowed the column.
		expect(screen.queryByText(/codexErrorInfo/i)).not.toBeInTheDocument();
		expect(screen.queryByText(/provider error:/i)).not.toBeInTheDocument();
	});

	it("reads already-normalized summary and detail without a JSON dump", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "error",
					status: "failed",
					summary: "Reconnecting... [1/5]",
					detail: {
						message: "Reconnecting... [1/5]",
						error:
							"stream disconnected before completion: You have no credits remaining. Add credits to continue using the API at https://platform.openai.com/settings/organization/billing",
					},
				})}
			/>,
		);
		expect(screen.getByText("Reconnecting... [1/5]")).toBeInTheDocument();
		expect(screen.getByText(/You have no credits remaining/i)).toBeInTheDocument();
		expect(screen.queryByText(/codexErrorInfo/i)).not.toBeInTheDocument();
	});

	it("keeps plain-language errors readable without a JSON dump", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "error",
					status: "failed",
					summary: "stream disconnected before completion: You have no credits remaining",
				})}
			/>,
		);
		expect(screen.getByText(/no credits remaining/i)).toBeInTheDocument();
	});

	it("stays inside the chat column instead of widening it", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "error",
					status: "failed",
					summary: stored,
				})}
			/>,
		);
		const row = screen.getByText("Reconnecting... [1/5]").closest("div.min-w-0.max-w-full");
		expect(row?.className).toMatch(/overflow-hidden/);
	});

	it("does not mark historical reconnect rows as live alerts", () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "error",
					status: "failed",
					summary: stored,
				})}
			/>,
		);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});
});

describe("command terminal input", () => {
	it("shows what the agent typed apart from what the command printed", async () => {
		render(
			<ActivityRow
				activity={activity({
					summary: "go test ./...",
					detail: {
						output: "running tests\n",
						outputSource: "stream",
						// One unprintable byte. Stripping it would leave an empty box where the
						// interesting thing happened.
						terminalInput: "\u0003",
					},
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("Agent typed")).toBeInTheDocument();
		expect(screen.getByText("^C")).toBeInTheDocument();
	});

	it("strips escape sequences out of command output", async () => {
		const { container } = render(
			<ActivityRow
				activity={activity({
					summary: "go test ./...",
					detail: {
						output: "\u001b[32mok\u001b[0m  pkg/a\ndownloading  10%\rdownloading 100%\n",
						outputSource: "aggregate",
					},
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		const pre = container.querySelector("pre");
		expect(pre?.textContent).toBe("ok  pkg/a\ndownloading 100%\n");
	});
});

describe("file changes", () => {
	const edit = activity({
		activityKind: "file_change",
		summary: "Edited 2 files",
		detail: {
			files: [
				{
					path: "src/a.ts",
					status: "modified",
					additions: 2,
					deletions: 1,
					patch: "@@ -1 +1,2 @@\n-old\n+new\n+more\n",
				},
				{ path: "src/gone.ts", status: "deleted", additions: 0, deletions: 40 },
			],
		},
	});

	it("shows a status per file, which no client could read before", async () => {
		render(<ActivityRow activity={edit} />);
		await userEvent.click(screen.getByRole("button", { name: /Edited/ }));
		expect(screen.getByLabelText("modified")).toBeInTheDocument();
		expect(screen.getByLabelText("deleted")).toBeInTheDocument();
	});

	it("opens a file's own patch", async () => {
		render(<ActivityRow activity={edit} />);
		await userEvent.click(screen.getByRole("button", { name: /Edited/ }));
		await userEvent.click(screen.getByRole("button", { name: /src\/a\.ts/ }));
		expect(screen.getByText(/@@ -1 \+1,2 @@/)).toBeInTheDocument();
	});

	// A file with no patch is not a button: a control that does nothing when pressed
	// is worse than plain text.
	it("does not offer to open a file that carries no patch", async () => {
		render(<ActivityRow activity={edit} />);
		await userEvent.click(screen.getByRole("button", { name: /Edited/ }));
		expect(screen.queryByRole("button", { name: /src\/gone\.ts/ })).not.toBeInTheDocument();
	});

	it("says a patch stops early rather than presenting it as the whole change", async () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "file_change",
					summary: "Edited 1 file",
					detail: {
						files: [
							{
								path: "src/big.ts",
								status: "added",
								additions: 900,
								deletions: 0,
								patch: "line\n".repeat(3),
								patchTruncated: true,
							},
						],
					},
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button", { name: /Edited/ }));
		// The row's own header names the single file too, so the file's disclosure is
		// the last of the two.
		await userEvent.click(screen.getAllByRole("button", { name: /src\/big\.ts/ }).at(-1)!);
		expect(screen.getByText(/longer than AO stores/i)).toBeInTheDocument();
	});

	// Rows written by earlier builds carry no status. The file is still drawn, as a
	// modification, rather than being dropped for lacking a field.
	it("draws a file whose status predates the daemon normalizing it", async () => {
		render(
			<ActivityRow
				activity={activity({
					activityKind: "file_change",
					summary: "Edited 2 files",
					detail: {
						files: [
							{ path: "src/legacy.ts", additions: 3, deletions: 1 },
							{ path: "src/other.ts", status: "added", additions: 1, deletions: 0 },
						],
					},
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button", { name: /Edited/ }));
		expect(screen.getByText("src/legacy.ts")).toBeInTheDocument();
		expect(screen.getByLabelText("modified")).toBeInTheDocument();
	});
});
