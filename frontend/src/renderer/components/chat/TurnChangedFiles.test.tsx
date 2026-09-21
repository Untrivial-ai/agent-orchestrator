import { render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ActivityRow, TurnChangedFiles } from "./ChatTimelineItems";
import { ActivityRun } from "./ActivityRun";
import type { ConversationActivity, TurnDiff } from "../../types/conversation";
import type { WorkspaceFileSummary } from "../../hooks/useSessionWorkspaceFiles";
import { useSessionWorkspaceChangedFiles } from "../../hooks/useSessionWorkspaceFiles";
import { TooltipProvider } from "../ui/tooltip";

// The card repo-qualifies each row against the session's changed files (read from
// cache, the same repo-qualified list the click resolver matches), so tests seed
// that list directly instead of relying on a client-side path heuristic.
vi.mock("../../hooks/useSessionWorkspaceFiles", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../hooks/useSessionWorkspaceFiles")>();
	return { ...actual, useSessionWorkspaceChangedFiles: vi.fn(() => [] as WorkspaceFileSummary[]) };
});

const mockWorkspaceFileList = vi.mocked(useSessionWorkspaceChangedFiles);

function seedWorkspaceFiles(paths: string[]) {
	mockWorkspaceFileList.mockReturnValue(
		paths.map((path) => ({ path, status: "added", additions: 1, deletions: 0 }) as WorkspaceFileSummary),
	);
}

beforeEach(() => {
	mockWorkspaceFileList.mockReturnValue([]);
});

afterEach(() => {
	vi.clearAllMocks();
});

function render(ui: ReactElement) {
	return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}

// These cover the two signal rules this surface exists to keep: a changed-file
// list never claims to be complete when it was cut, and command output only adds
// a warning when AO actually stopped storing it.

function diff(overrides: Partial<TurnDiff> = {}): TurnDiff {
	return {
		files: [
			{ path: "src/a.ts", additions: 12, deletions: 3, status: "modified" },
			{ path: "src/new.ts", additions: 40, deletions: 0, status: "added" },
		],
		...overrides,
	};
}

function commandActivity(
	detail: ConversationActivity["detail"],
	status: ConversationActivity["status"] = "completed",
): ConversationActivity {
	return {
		kind: "activity",
		id: "act-1",
		sequence: 1,
		revision: 1,
		activityKind: "command",
		status,
		summary: "go test ./...",
		detail,
		createdAt: new Date().toISOString(),
	};
}

describe("TurnChangedFiles", () => {
	it("shows the bordered summary with files visible", () => {
		render(<TurnChangedFiles diff={diff()} />);
		expect(screen.getByText("2 Files Changed")).toBeInTheDocument();
		expect(screen.getByText("src/a.ts")).toBeInTheDocument();
		expect(screen.getByText("src/new.ts")).toBeInTheDocument();
		expect(screen.getByText("+12")).toBeInTheDocument();
		expect(screen.getByText("+40")).toBeInTheDocument();
		expect(screen.getByText("−3")).toBeInTheDocument();
	});

	// In a real multi-repo workspace the daemon stores the provider's repo-relative
	// path verbatim (`workspace-test.txt`), with no repository qualifier. The card
	// resolves the row against the session workspace file list, the same list the
	// click handler matches, and shows that repository-qualified path
	// (`alpha/workspace-test.txt`) rather than the bare basename the daemon stored.
	it("shows the repository-qualified path resolved from the workspace file list", () => {
		seedWorkspaceFiles(["alpha/workspace-test.txt"]);
		render(
			<TurnChangedFiles
				sessionId="session-1"
				diff={{
					files: [{ path: "workspace-test.txt", additions: 1, deletions: 0, status: "added" }],
				}}
			/>,
		);
		expect(screen.getByText("alpha/workspace-test.txt")).toBeInTheDocument();
		expect(screen.queryByText("workspace-test.txt")).not.toBeInTheDocument();
	});

	// The label the row shows and the path it opens must be the same repository-qualified
	// string, so a click cannot open a different file than the one named on screen.
	it("shows and opens the same repository-qualified path", async () => {
		seedWorkspaceFiles(["alpha/workspace-test.txt"]);
		const onOpenFile = vi.fn();
		render(
			<TurnChangedFiles
				sessionId="session-1"
				diff={{
					files: [{ path: "workspace-test.txt", additions: 1, deletions: 0, status: "added" }],
				}}
				onOpenFile={onOpenFile}
			/>,
		);
		expect(screen.getByText("alpha/workspace-test.txt")).toBeInTheDocument();
		await userEvent.click(
			screen.getByRole("button", { name: /Open alpha\/workspace-test\.txt in Files/ }),
		);
		expect(onOpenFile).toHaveBeenCalledWith("alpha/workspace-test.txt");
	});

	// Two repos in one workspace each change a same-named file. The daemon stores the
	// provider path verbatim, so both diff rows arrive as the byte-identical bare
	// `workspace-test.txt` and no display-layer resolver can tell them apart. The card
	// must show the honest bare basename for both rather than guess a repo and name
	// the wrong one (the #5366 mislabel). Full disambiguation needs a repo-qualified
	// path from the daemon, which the diff payload does not carry.
	it("keeps the honest bare path when two repos change a same-named file", () => {
		seedWorkspaceFiles(["alpha/workspace-test.txt", "beta/workspace-test.txt"]);
		render(
			<TurnChangedFiles
				sessionId="session-1"
				diff={{
					files: [
						{ path: "workspace-test.txt", additions: 1, deletions: 0, status: "added" },
						{ path: "workspace-test.txt", additions: 2, deletions: 0, status: "added" },
					],
				}}
			/>,
		);
		expect(screen.getAllByText("workspace-test.txt")).toHaveLength(2);
		expect(screen.queryByText("alpha/workspace-test.txt")).not.toBeInTheDocument();
		expect(screen.queryByText("beta/workspace-test.txt")).not.toBeInTheDocument();
	});

	it("offers Review when a handler is provided", async () => {
		const onReview = vi.fn();
		render(<TurnChangedFiles diff={diff()} onReview={onReview} />);
		await userEvent.click(screen.getByRole("button", { name: "Review" }));
		expect(onReview).toHaveBeenCalledTimes(1);
	});

	it("opens a file in the Files panel when clicked", async () => {
		const onOpenFile = vi.fn();
		render(<TurnChangedFiles diff={diff()} onOpenFile={onOpenFile} />);
		await userEvent.click(screen.getByRole("button", { name: /Open src\/a\.ts in Files/ }));
		expect(onOpenFile).toHaveBeenCalledWith("src/a.ts");
	});

	// With no workspace list loaded the row falls back to the raw path the daemon
	// stored, so the label and open target stay in sync with what is available.
	it("falls back to the raw row path when no workspace list is available", async () => {
		const onOpenFile = vi.fn();
		render(
			<TurnChangedFiles
				diff={{
					files: [{ path: "notes.txt", additions: 1, deletions: 0, status: "added" }],
				}}
				items={[
					{
						kind: "activity",
						id: "a-1",
						sequence: 1,
						revision: 0,
						activityKind: "command",
						status: "completed",
						summary: "Ran command",
						detail: { cwd: "/Users/me/.ao/dev/data/worktrees/demo/demo-1", command: "ls" },
						createdAt: new Date().toISOString(),
					},
				]}
				onOpenFile={onOpenFile}
			/>,
		);
		await userEvent.click(screen.getByRole("button", { name: /Open notes\.txt in Files/ }));
		expect(onOpenFile).toHaveBeenCalledWith("notes.txt");
	});

	it("shows the full path on hover", async () => {
		const user = userEvent.setup();
		render(<TurnChangedFiles diff={diff()} />);
		await user.hover(screen.getByText("src/a.ts"));
		expect(await screen.findByRole("tooltip")).toHaveTextContent("src/a.ts");
	});

	// With no command cwd in the turn there is no reliable worktree root to trim
	// against, so the row shows the plain basename rather than a fabricated
	// `wexaai-21/...` prefix (that leading segment is the worktree directory, not a
	// workspace path). The tooltip still carries the full absolute path.
	it("resolves a turn-diff basename against the turn's Edited path for the tooltip", async () => {
		const user = userEvent.setup();
		render(
			<TurnChangedFiles
				diff={{
					files: [{ path: "random_words_1.txt", additions: 50, deletions: 0, status: "added" }],
				}}
				items={[
					{
						kind: "activity",
						id: "a-1",
						sequence: 1,
						revision: 0,
						activityKind: "file_change",
						status: "completed",
						summary: "Edited 1 file",
						detail: {
							files: [
								{
									path: "/Users/vaanyagoel/.ao/dev/data/worktrees/wexaai/wexaai-21/random_words_1.txt",
									status: "added",
									additions: 50,
									deletions: 0,
								},
							],
						},
						createdAt: new Date().toISOString(),
					},
				]}
			/>,
		);
		await user.hover(screen.getByText("random_words_1.txt"));
		expect(await screen.findByRole("tooltip")).toHaveTextContent(
			"~/.ao/dev/data/worktrees/wexaai/wexaai-21/random_words_1.txt",
		);
	});

	it("joins a command cwd when the turn diff only has a relative path", async () => {
		const user = userEvent.setup();
		render(
			<TurnChangedFiles
				diff={{
					files: [{ path: "notes.txt", additions: 1, deletions: 0, status: "added" }],
				}}
				items={[
					{
						kind: "activity",
						id: "a-1",
						sequence: 1,
						revision: 0,
						activityKind: "command",
						status: "completed",
						summary: "Ran command",
						detail: { cwd: "/Users/me/.ao/dev/data/worktrees/demo/demo-1", command: "ls" },
						createdAt: new Date().toISOString(),
					},
				]}
			/>,
		);
		await user.hover(screen.getByText("notes.txt"));
		expect(await screen.findByRole("tooltip")).toHaveTextContent(
			"~/.ao/dev/data/worktrees/demo/demo-1/notes.txt",
		);
	});

	it("shows both ends of a rename in the path tooltip", async () => {
		const user = userEvent.setup();
		render(
			<TurnChangedFiles
				diff={{
					files: [
						{ path: "src/new.ts", oldPath: "src/old.ts", additions: 1, deletions: 1, status: "renamed" },
					],
				}}
			/>,
		);
		expect(screen.getByText("1 File Changed")).toBeInTheDocument();
		expect(screen.getByText("src/new.ts")).toBeInTheDocument();
		await user.hover(screen.getByText("src/new.ts"));
		expect(await screen.findByRole("tooltip")).toHaveTextContent("src/old.ts → src/new.ts");
	});

	it("says the list was cut rather than presenting it as the whole change", () => {
		render(<TurnChangedFiles diff={diff({ truncated: true })} onReview={() => {}} />);
		expect(screen.getByText(/changed more files than AO lists/i)).toBeInTheDocument();
		expect(screen.getByText(/Use Review for the whole change/i)).toBeInTheDocument();
	});

	it("expands beyond the preview with Show N more", async () => {
		const many: TurnDiff = {
			files: Array.from({ length: 6 }, (_, i) => ({
				path: `src/file-${i}.ts`,
				additions: i + 1,
				deletions: 0,
				status: "modified" as const,
			})),
		};
		render(<TurnChangedFiles diff={many} />);
		expect(screen.getByText("src/file-0.ts")).toBeInTheDocument();
		expect(screen.queryByText("src/file-5.ts")).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Show 2 more" }));
		expect(screen.getByText("src/file-5.ts")).toBeInTheDocument();
	});

	it("marks a running turn's diff as still growing", () => {
		render(<TurnChangedFiles diff={diff()} live />);
		expect(screen.getByLabelText("still changing")).toBeInTheDocument();
	});

	// An agent that reports no diff must not get an empty panel implying it changed
	// nothing.
	it("renders nothing when the turn changed no files", () => {
		const { container } = render(<TurnChangedFiles diff={{ files: [] }} />);
		expect(container).toBeEmptyDOMElement();
	});
});

describe("ActivityRow command output", () => {
	it("opens itself while a command is still printing", () => {
		const { container } = render(
			<ActivityRow
				activity={commandActivity(
					{ output: "ok  pkg/a\n", outputSource: "stream", outputMayBePartial: true },
					"running",
				)}
			/>,
		);
		// No click: live output that needs a click is not live. Read off the <pre>
		// rather than via getByText, which normalizes the runs of whitespace that
		// real command output is full of.
		const pre = container.querySelector("pre");
		expect(pre?.textContent).toBe("ok  pkg/a\n");
	});

	it("does not add provider provenance below streamed output", () => {
		render(
			<ActivityRow
				activity={commandActivity(
					{ output: "tick-2\n", outputSource: "stream", outputMayBePartial: true },
					"running",
				)}
			/>,
		);
		expect(screen.getByText("tick-2")).toBeInTheDocument();
		expect(screen.queryByText(/Streamed live as the command runs/i)).not.toBeInTheDocument();
	});

	it("does not add provider provenance below aggregate output", async () => {
		render(
			<ActivityRow
				activity={commandActivity({
					output: "done\n",
					outputSource: "aggregate",
					outputMayBePartial: true,
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("done")).toBeInTheDocument();
		expect(screen.queryByText(/Rolled up by the provider after the command finished/i)).not.toBeInTheDocument();
	});

	it("warns when output hit the storage cap", async () => {
		render(
			<ActivityRow
				activity={commandActivity({
					output: "x".repeat(64),
					outputSource: "stream",
					outputMayBePartial: true,
					outputTruncated: true,
				})}
			/>,
		);
		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText(/printed more than AO stores/i)).toBeInTheDocument();
	});

	it("keeps a finished command collapsed so the timeline stays readable", () => {
		render(
			<ActivityRow
				activity={commandActivity({ output: "ok\n", outputSource: "aggregate" }, "completed")}
			/>,
		);
		expect(screen.queryByText("ok")).not.toBeInTheDocument();
	});

	it("renders structured ACP output from conversations written by older builds", async () => {
		const structuredOutput = {
			metadata: { exit: 0, output: "metadata copy" },
			output: "command output\n",
		};
		const activity = commandActivity({ output: structuredOutput as unknown as string });
		render(<ActivityRow activity={activity} />);

		await userEvent.click(screen.getByRole("button"));
		expect(screen.getByText("command output")).toBeInTheDocument();
	});
});

// A run collapses consecutive tool calls to one line. Without the same auto-open
// rule, a command streaming output inside a run is live to nobody.
describe("ActivityRun with a streaming command", () => {
	function plan(id: string): ConversationActivity {
		return {
			kind: "activity",
			id,
			sequence: 2,
			revision: 0,
			activityKind: "plan",
			status: "completed",
			summary: "Updated plan",
			createdAt: new Date().toISOString(),
		};
	}

	it("opens itself so live output inside it is visible", () => {
		render(
			<ActivityRun
				activities={[
					commandActivity(
						{ output: "compiling…\n", outputSource: "stream", outputMayBePartial: true },
						"running",
					),
					plan("act-2"),
				]}
			/>,
		);
		expect(screen.getByText("compiling…")).toBeInTheDocument();
	});

	it("opens the matching subgroup when a grouped command is streaming", () => {
		const completedCommand = commandActivity({ command: "pwd" }, "completed");
		completedCommand.id = "act-2";
		completedCommand.sequence = 2;

		render(
			<ActivityRun
				activities={[
					commandActivity(
						{ command: "npm run build", output: "compiling…\n", outputSource: "stream", outputMayBePartial: true },
						"running",
					),
					completedCommand,
					plan("act-3"),
				]}
			/>,
		);

		expect(screen.getByText("compiling…")).toBeInTheDocument();
	});

	it("stays collapsed when nothing inside it is printing", () => {
		const { container } = render(
			<ActivityRun
				activities={[
					commandActivity({ output: "done\n", outputSource: "aggregate" }, "completed"),
					plan("act-2"),
				]}
			/>,
		);
		expect(container.querySelector("pre")).toBeNull();
	});

	it("summarizes grouped non-zero command exits without destructive styling", () => {
		const secondCommand = commandActivity(
			{ command: "npm run typecheck", exitCode: 2 },
			"failed",
		);
		secondCommand.id = "act-2";
		secondCommand.sequence = 2;

		render(
			<ActivityRun
				activities={[
					commandActivity({ command: "npm test", exitCode: 1 }, "failed"),
					secondCommand,
				]}
			/>,
		);

		expect(screen.getByText("2 exited")).toHaveClass("text-muted-foreground/70");
		expect(screen.getByText("2 exited")).not.toHaveClass("text-destructive");
		expect(screen.queryByText("2 failed")).not.toBeInTheDocument();
	});

	it("keeps real grouped failures destructive when mixed with a command exit", () => {
		const failedPlan = plan("act-2");
		failedPlan.status = "failed";

		render(
			<ActivityRun
				activities={[
					commandActivity({ command: "npm test", exitCode: 1 }, "failed"),
					failedPlan,
				]}
			/>,
		);

		expect(screen.getByText("1 exited")).toHaveClass("text-muted-foreground/70");
		expect(screen.getByText("1 failed")).toHaveClass("text-destructive");
	});
});

describe("ActivityRow command labels", () => {
	it("describes read-only shell inspection as file exploration", () => {
		render(
			<ActivityRow
				activity={commandActivity(
					{
						command:
							"sed -n '1,240p' ~/.ao/dev/data/skills/using-ao/SKILL.md && sed -n '1,240p' ~/.ao/dev/data/skills/other/SKILL.md",
						output: "skill contents",
					},
					"completed",
				)}
			/>,
		);
		expect(screen.getByText("Read files")).toBeInTheDocument();
		expect(screen.queryByText(/sed -n/)).not.toBeInTheDocument();
	});

	it("describes execution compactly, then reveals the exact command", async () => {
		const user = userEvent.setup();
		render(
			<ActivityRow
				activity={commandActivity({ command: "go test ./...", output: "ok" }, "completed")}
			/>,
		);
		expect(screen.getByText("Ran command")).toBeInTheDocument();
		expect(screen.queryByText("go test ./...")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: /Ran command/ }));
		expect(screen.getByText("go test ./...")).toBeInTheDocument();
		expect(screen.getByText("ok")).toBeInTheDocument();
	});

	it("keeps a command-only row expandable when the provider reports no output", async () => {
		const user = userEvent.setup();
		render(
			<ActivityRow
				activity={commandActivity({ command: `printf "%s" "hello world"` }, "completed")}
			/>,
		);

		const row = screen.getByRole("button", { name: /Ran command/ });
		expect(row).toBeEnabled();
		expect(screen.queryByText(`printf "%s" "hello world"`)).not.toBeInTheDocument();

		await user.click(row);
		expect(screen.getByText(`printf "%s" "hello world"`)).toBeInTheDocument();
	});

	it("uses the same compact treatment as a grouped command summary", () => {
		const { container } = render(
			<ActivityRow
				activity={commandActivity(
					{ command: "git status --short", output: "fatal: not a repository", exitCode: 1 },
					"failed",
				)}
			/>,
		);
		const row = screen.getByRole("button");
		expect(row).toHaveClass("py-0.5", "gap-1.5", "select-none");
		expect(screen.getByText("Checked repository")).toHaveClass(
			"text-[11.5px]",
			"font-normal",
			"text-muted-foreground",
		);
		expect(screen.getByText("exit 1")).toHaveClass("text-muted-foreground/70");
		expect(screen.getByText("exit 1")).not.toHaveClass("text-destructive");
		expect(container.querySelector(".lucide-square-terminal")).toBeNull();
		expect(row.querySelector(".flex-1")).toBeNull();
		expect(row.textContent).toMatch(/^Checked repositoryexit 1/);
	});

	it("keeps command failures without exit metadata destructive", () => {
		render(
			<ActivityRow
				activity={commandActivity({ command: "search the web", reason: "provider error" }, "failed")}
			/>,
		);

		expect(screen.getByText("failed")).toHaveClass("text-destructive");
		expect(screen.getByText("failed")).not.toHaveClass("text-muted-foreground/70");
	});

	it("shows an interrupted command as stopped instead of leaving a live spinner", () => {
		render(
			<ActivityRow
				activity={commandActivity({ command: "sleep 60", output: "started\n" }, "cancelled")}
			/>,
		);
		expect(screen.getByText("stopped")).toBeInTheDocument();
		expect(screen.queryByLabelText("running")).not.toBeInTheDocument();
	});
});
