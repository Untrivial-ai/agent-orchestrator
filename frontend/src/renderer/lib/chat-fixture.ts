/**
 * A conversation fixture for developing the Chat surface before the daemon can
 * serve one.
 *
 * The shapes are taken from a real `codex app-server` session (codex-cli
 * 0.146.0), not invented — including the details that are easy to get wrong:
 *
 *  - the approval offers `accept`, an object-shaped `acceptWithExecpolicyAmendment`,
 *    and `cancel`, with **no decline**, which is what the provider actually sent;
 *  - the command arrives wrapped in a login shell (`/bin/zsh -lc '…'`);
 *  - a command's captured output is flagged as possibly partial, because the
 *    provider's own aggregation dropped its leading bytes;
 *  - one `commandExecution` is left `running` with no completion, because the
 *    provider does start commands it then supersedes.
 */

import type {
	ConversationItem,
	ConversationSnapshot,
	ConversationTurn,
} from "../types/conversation";

/**
 * Timestamps are relative to load so the live turn's elapsed counter reads
 * realistically instead of clamping at zero. `minute` counts forward from the
 * start of the conversation, which is placed nine minutes in the past.
 */
const CONVERSATION_START = Date.now() - 9 * 60_000;

const t = (minute: number, second = 0): string =>
	new Date(CONVERSATION_START + (minute - 31) * 60_000 + second * 1000).toISOString();

export const chatFixture: ConversationSnapshot = {
	conversationId: "conv-ao-14",
	sessionId: "ao-14",
	harness: "codex",
	mode: "chat",
	controller: { state: "busy" },
	latestSequence: 14,
	oldestSequence: 1,
	hasMoreBefore: false,
	activeBranchId: "branch-root",
	branchPoints: [],
	settings: { model: "gpt-5.6-terra", reasoningEffort: "high" },
	// Healthy servers as the baseline, so the failed-server fixture is visibly the
	// exception rather than the only time this field is populated.
	mcpServers: [
		{ name: "github", status: "ready" },
		{ name: "playwright", status: "ready" },
	],
	account: { authMode: "chatgpt", planLabel: "Pro" },
	threadState: { status: "active" },
	// What a live codex controller advertises. Controls gate on this rather than on
	// the harness name, so a fixture without it draws no steer or reload control —
	// which is exactly what a session whose controller has not reported yet does.
	capabilities: [
		"approvals",
		"compaction",
		"diffs",
		"fork",
		"history",
		"interrupt",
		"mcp_reload",
		"models",
		"plans",
		"rate_limits",
		"rename",
		"resume",
		"rollback",
		"skills",
		"steer",
		"streaming",
		"tools",
		"usage",
		"user_input",
	],
	turns: [
		{
			id: "turn-1",
			state: "completed",
			providerTurnId: "019fbdd1-a5fc-7f93",
			requestedAt: t(31),
			startedAt: t(31, 1),
			completedAt: t(33, 12),
			// A settled turn diff, parsed by the daemon out of the one aggregated
			// unified diff string `turn/diff/updated` carries.
			diff: {
				files: [
					{ path: "backend/internal/ports/chat.go", additions: 34, deletions: 2, status: "modified" },
					{ path: "backend/internal/adapters/chatdriver/codexappserver/diff.go", additions: 128, deletions: 0, status: "added" },
					{ path: "backend/internal/chat/legacy_shim.go", additions: 0, deletions: 61, status: "deleted" },
					{ path: "backend/internal/chat/normalize.go", oldPath: "backend/internal/chat/translate.go", additions: 4, deletions: 4, status: "renamed" },
				],
			},
		},
		{
			id: "turn-2",
			state: "running",
			providerTurnId: "019fbdd1-fdac-76f2",
			requestedAt: t(38),
			startedAt: t(38, 1),
			// Still running, so the list can still grow. The panel says so rather than
			// looking like a final answer.
			diff: {
				files: [
					{ path: "backend/internal/storage/sqlite/queries/conversations.sql", additions: 27, deletions: 0, status: "modified" },
				],
				truncated: true,
			},
			// A plan mid-flight: one step done, one running, two waiting. On the turn
			// rather than in the timeline, because the provider re-sends the whole thing
			// on every revision and the daemon overwrites it.
			plan: {
				explanation: "Land the store first so the endpoint has something to read.",
				steps: [
					{ text: "Land the conversation store and its migration", status: "completed" },
					{ text: "Run the backend tests", status: "in_progress" },
					{ text: "Add the snapshot endpoint", status: "pending" },
					{ text: "Delegate the HTTP layer to a worker", status: "pending" },
				],
			},
		},
	],
	items: [
		{
			kind: "message",
			id: "m-1",
			turnId: "turn-1",
			sequence: 1,
			revision: 0,
			role: "user",
			origin: "human",
			text: "Check the worktree state and tell me what changed since the base commit.",
			content: [],
			editAvailable: true,
			streaming: false,
			delivery: "accepted",
			createdAt: t(31),
		},
		{
			kind: "activity",
			id: "a-1",
			turnId: "turn-1",
			sequence: 2,
			revision: 0,
			activityKind: "reasoning",
			status: "completed",
			summary: "Reasoning",
			// A settled summary in the shape the provider really sends: markdown with a
			// bolded section header. It only arrives at all when the user's own codex
			// config asks for summaries — `chatFixtureReasoningEmpty` is the default
			// install, which is the case the UI has to explain rather than show.
			detail: {
				text:
					"**Reading the worktree first**\n\n" +
					"The question is what changed since the base commit, so `git status` is the " +
					"cheapest first look. If anything is staged I will need `git diff --cached` " +
					"as well; otherwise the unstaged diff is the whole answer.",
			},
			createdAt: t(31, 4),
		},
		{
			kind: "activity",
			id: "a-2",
			turnId: "turn-1",
			sequence: 3,
			revision: 1,
			activityKind: "command",
			status: "completed",
			summary: "git status --short",
			detail: {
				command: "git status --short",
				rawCommand: "/bin/zsh -lc 'git status --short'",
				cwd: "/Users/dhruv/.ao/data/worktrees/agent-orchestrator-1/ao-14",
				output: " M backend/internal/session_manager/manager.go\n M backend/internal/ports/chat.go\n",
				outputMayBePartial: true,
				outputSource: "aggregate",
				exitCode: 0,
				durationMs: 31,
			},
			createdAt: t(31, 8),
		},
		{
			kind: "activity",
			id: "a-3",
			turnId: "turn-1",
			sequence: 4,
			revision: 0,
			activityKind: "file_change",
			status: "completed",
			summary: "Edited 2 files",
			// Normalized by the daemon: a plain status where the provider sends an
			// object, and the file's own patch alongside the counts. Both shapes the
			// provider emits are here — an update carries a unified hunk, an add carries
			// the new file's whole contents with no hunk header at all.
			detail: {
				files: [
					{
						path: "backend/internal/session_manager/manager.go",
						status: "modified",
						additions: 4,
						deletions: 1,
						patch:
							"@@ -142,7 +142,10 @@ func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (*Session, error)\n" +
							" \tif req.Project == \"\" {\n" +
							"-\t\treturn nil, ErrProjectRequired\n" +
							"+\t\treturn nil, fmt.Errorf(\"spawn: %w\", ErrProjectRequired)\n" +
							"+\t}\n" +
							"+\tif req.Mode == ModeChat && !m.agents.SupportsChat(req.Agent) {\n" +
							"+\t\treturn nil, fmt.Errorf(\"spawn: %w\", ErrChatUnsupported)\n" +
							" \t}\n" +
							" \ttree, err := m.worktrees.Create(ctx, req.Project, req.Branch)\n",
					},
					{
						path: "backend/internal/ports/chat.go",
						status: "added",
						additions: 6,
						deletions: 0,
						// A new file's patch is its contents, with no hunk header.
						patch:
							"package ports\n\n" +
							"// ChatDriver is one provider's conversation, in AO's vocabulary.\n" +
							"type ChatDriver interface {\n" +
							"\tSend(ctx context.Context, msg ChatUserMessage) (ChatTurnRef, error)\n" +
							"}\n",
						patchTruncated: true,
					},
				],
			},
			createdAt: t(32, 2),
		},
		{
			kind: "message",
			id: "m-2",
			turnId: "turn-1",
			sequence: 5,
			revision: 7,
			role: "assistant",
			origin: "provider",
			text:
				"Two files are modified against the base commit. `manager.go` carries the spawn split, " +
				"and `chat.go` adds the driver port. Nothing is staged yet, so the worktree is safe to reset " +
				"if you want to start over.\n\n" +
				"```go\n" +
				"// Spawn hands the worker its own worktree before the agent starts.\n" +
				"func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (*Session, error) {\n" +
				'\tif req.Project == "" {\n' +
				'\t\treturn nil, fmt.Errorf("spawn: %w", ErrProjectRequired)\n' +
				"\t}\n" +
				"\ttree, err := m.worktrees.Create(ctx, req.Project, req.Branch)\n" +
				"\tif err != nil {\n" +
				"\t\treturn nil, err\n" +
				"\t}\n" +
				"\treturn m.start(ctx, tree, req.Kind == KindOrchestrator)\n" +
				"}\n" +
				"```\n\n" +
				"The port itself is small:\n\n" +
				"```ts\n" +
				"export interface ChatDriver {\n" +
				"\tsend(text: string, settings?: TurnSettings): Promise<TurnId>;\n" +
				"\tresolve(requestId: string, decisionId: string): Promise<void>;\n" +
				"\tinterrupt(): Promise<void>;\n" +
				"}\n" +
				"```\n\n" +
				"One line you will want to wrap rather than scroll:\n\n" +
				"```sh\n" +
				"AO_DATA_DIR=~/.ao go test ./internal/... -run 'TestConversation|TestSpawn' -count=1 -race -timeout 300s -coverprofile=/tmp/ao-cover.out && go tool cover -func=/tmp/ao-cover.out | tail -1\n" +
				"```\n\n" +
				"A fence with no language is still a block, not inline code:\n\n" +
				"```\n" +
				"ok  \tgithub.com/aoagents/ao/internal/domain\t0.412s\n" +
				"```",
			streaming: false,
			createdAt: t(32, 30),
		},
		{
			kind: "activity",
			id: "a-4",
			turnId: "turn-1",
			sequence: 6,
			revision: 0,
			activityKind: "usage",
			status: "completed",
			summary: "Token usage updated",
			detail: { inputTokens: 12_480, outputTokens: 612, totalTokens: 13_092 },
			createdAt: t(33, 10),
		},
		{
			kind: "message",
			id: "m-3",
			turnId: "turn-2",
			sequence: 7,
			revision: 0,
			role: "user",
			origin: "human",
			text: "Run the backend tests, then spawn a worker to write the HTTP layer.",
			content: [],
			editAvailable: true,
			streaming: false,
			delivery: "accepted",
			createdAt: t(38),
		},
		{
			kind: "message",
			id: "m-4",
			sequence: 8,
			revision: 0,
			role: "user",
			origin: "automation",
			senderLabel: "CI · agent-orchestrator #3431",
			text: "Checks failed on the base branch: lint (golangci-lint) exited 1.",
			streaming: false,
			delivery: "accepted",
			createdAt: t(38, 20),
		},
		{
			kind: "activity",
			id: "a-5",
			turnId: "turn-2",
			sequence: 9,
			revision: 0,
			activityKind: "command",
			status: "running",
			summary: "go test ./internal/...",
			detail: {
				command: "go test ./internal/...",
				rawCommand: "/bin/sh -c 'go test ./internal/...'",
				cwd: "/Users/dhruv/.ao/data/worktrees/agent-orchestrator-1/ao-14",
				// A command still running, already printing. Before output deltas were
				// accumulated there was nothing to show here until it finished, because
				// the provider's aggregate does not exist until completion.
				//
				// Carrying its escape sequences, because that is how output arrives:
				// nothing in the stack strips them. `go test` colours its verdicts, the
				// codex PTY inserts `\x1b[@` runs, and a progress line redraws itself with
				// carriage returns. Rendered verbatim this row is unreadable, which is what
				// `lib/ansi.ts` exists to fix. The bare `[@` is deliberate: the widely
				// copied ansi-regex omits `@` from its CSI final-byte class.
				output:
					"\u001b[?25l\u001b[2K\u001b[@\u001b[0m" +
					"\u001b[32mok\u001b[0m  \tgithub.com/aoagents/agent-orchestrator/backend/internal/domain\t0.412s\n" +
					"\u001b[32mok\u001b[0m  \tgithub.com/aoagents/agent-orchestrator/backend/internal/ports\t0.286s\n" +
					"downloading modules  12%\rdownloading modules  57%\rdownloading modules 100%\n" +
					"\u001b[32mok\u001b[0m  \tgithub.com/aoagents/agent-orchestrator/backend/internal/service/chat\t11.554s\n",
				outputSource: "stream",
				outputMayBePartial: true,
				// What the agent typed at the running command, not what the command
				// printed. Kept apart because the PTY echoes keystrokes.
				terminalInput: "\u0003",
			},
			createdAt: t(38, 41),
		},
		{
			kind: "activity",
			id: "a-mcp-1",
			turnId: "turn-2",
			sequence: 10,
			revision: 0,
			activityKind: "mcp_tool",
			status: "completed",
			summary: "search_issues",
			// A tool served by an MCP server. It used to render as though the agent had
			// run a shell command in the worktree, which claimed something that never
			// happened.
			detail: {
				server: "github",
				toolName: "search_issues",
				arguments: { repo: "aoagents/agent-orchestrator", state: "open", labels: ["chat-mode"] },
				result: {
					total: 2,
					issues: [
						{ number: 3360, title: "Chat session mode: render the new provider signal" },
						{ number: 3369, title: "Automatic semantic task titles" },
					],
				},
				success: true,
			},
			createdAt: t(39, 1),
		},
		{
			kind: "activity",
			id: "a-7",
			turnId: "turn-2",
			sequence: 11,
			revision: 0,
			activityKind: "approval",
			status: "pending",
			summary: "Run ao spawn --project agent-orchestrator-1 --name http-layer",
			requestId: "0",
			// Exactly what the provider offered in the captured session: no decline,
			// and one object-shaped decision carrying a policy amendment.
			decisions: [
				{ id: "accept", label: "Approve" },
				{ id: "acceptWithExecpolicyAmendment", label: "Approve and remember this command" },
				{ id: "cancel", label: "Cancel" },
			],
			detail: {
				command: "ao spawn --project agent-orchestrator-1 --name http-layer --prompt '…'",
				rawCommand:
					"/bin/zsh -lc \"ao spawn --project agent-orchestrator-1 --name http-layer --prompt '…'\"",
				cwd: "/Users/dhruv/.ao/data/worktrees/agent-orchestrator-1/ao-14",
				reason: "Delegate the conversation HTTP layer to a new worker",
			},
			createdAt: t(39, 18),
		},
		{
			kind: "message",
			id: "m-5",
			turnId: "turn-2",
			sequence: 12,
			revision: 3,
			role: "assistant",
			origin: "provider",
			text: "Tests are still running. I need approval before spawning the worker, since",
			streaming: true,
			createdAt: t(39, 24),
		},
		{
			kind: "activity",
			id: "a-steer-1",
			turnId: "turn-2",
			sequence: 13,
			revision: 0,
			// A `system` activity by storage and the user's own words by meaning. AO
			// records a steer as an activity because a message would have opened a second
			// turn; the timeline reads the `event` discriminator, not the kind.
			activityKind: "system",
			status: "completed",
			summary: "Skip the integration tests — just the unit ones",
			detail: {
				event: "steer",
				text: "Skip the integration tests — just the unit ones, they take four minutes.",
				origin: "human",
				clientMessageId: "8f2b1c44-0f2a-4a7f-9f0a-1b2c3d4e5f60",
			},
			createdAt: t(39, 31),
		},
		{
			kind: "activity",
			id: "a-review-1",
			turnId: "turn-2",
			sequence: 14,
			revision: 0,
			activityKind: "auto_review",
			status: "completed",
			// Captured against approvalsReviewer=auto_review: the provider decided this
			// on the user's behalf and never asked.
			summary: "curl -s https://proxy.golang.org/github.com/…/@v/list",
			detail: {
				reviewId: "rev-018f2c",
				targetItemId: "item-91",
				actionType: "command",
				status: "approved",
				command: "curl -s https://proxy.golang.org/github.com/aoagents/ao/@v/list",
				cwd: "/Users/dhruv/.ao/data/worktrees/agent-orchestrator-1/ao-14",
				riskLevel: "low",
				rationale:
					"Read-only request to the Go module proxy. It fetches a version list, writes nothing, and the host is on the allow list.",
				decisionSource: "agent",
				durationMs: 812,
			},
			createdAt: t(39, 36),
		},
	],
};

/** A second fixture: the controller died mid-turn, which must not read as idle. */
export const chatFixtureRecovering: ConversationSnapshot = {
	...chatFixture,
	controller: {
		state: "recovering",
		error: "app-server exited during a turn; reconnecting",
	},
	turns: [{ ...chatFixture.turns[0]! }, { ...chatFixture.turns[1]!, state: "running" }],
};

/**
 * A settled conversation: nothing in flight, and the thread has a name.
 *
 * This is the state the history operations live in. Undo is only offered while the
 * agent is idle, so the live fixture (which is mid-turn on purpose) cannot show it.
 */
export const chatFixtureSettled: ConversationSnapshot = {
	...chatFixture,
	controller: { state: "ready" },
	title: "Restore Canvas Renderer Fallback",
	turns: chatFixture.turns.map((turn) =>
		turn.state === "running"
			? { ...turn, state: "completed" as const, completedAt: t(39, 40) }
			: turn,
	),
};

/**
 * A long conversation, for the case the fixtures above cannot show: an
 * orchestrator session that has been running for hours.
 *
 * Generated rather than transcribed because the point is the shape and the count,
 * not the content — a real capture of this length would be thousands of lines of
 * fixture nobody reads. The proportions are taken from real sessions: a few tool
 * calls per turn, prose at the end of most of them, code in about one in three.
 */
export function chatFixtureLongHistory(turns: number): ConversationSnapshot {
	const items: ConversationItem[] = [];
	const conversationTurns: ConversationTurn[] = [];
	let sequence = 0;

	for (let index = 0; index < turns; index += 1) {
		const turnId = `turn-h${index}`;
		const minute = index * 3;
		conversationTurns.push({
			id: turnId,
			state: "completed",
			requestedAt: t(minute),
			startedAt: t(minute, 1),
			completedAt: t(minute, 40 + (index % 20)),
		});

		items.push({
			kind: "message",
			id: `hm-${index}`,
			turnId,
			sequence: (sequence += 1),
			revision: 0,
			role: "user",
			origin: "human",
			text: `${QUESTIONS[index % QUESTIONS.length]} (round ${index + 1})`,
			streaming: false,
			delivery: "accepted",
			createdAt: t(minute),
		});

		for (let call = 0; call < 3 + (index % 3); call += 1) {
			items.push({
				kind: "activity",
				id: `ha-${index}-${call}`,
				turnId,
				sequence: (sequence += 1),
				revision: 0,
				activityKind: "command",
				status: "completed",
				summary: COMMANDS[(index + call) % COMMANDS.length]!,
				detail: {
					command: COMMANDS[(index + call) % COMMANDS.length]!,
					output: `line one\nline two\nline three\n`,
					exitCode: 0,
					durationMs: 12 + call * 7,
				},
				createdAt: t(minute, 5 + call * 3),
			});
		}

		items.push({
			kind: "activity",
			id: `hf-${index}`,
			turnId,
			sequence: (sequence += 1),
			revision: 0,
			activityKind: "file_change",
			status: "completed",
			summary: "Edited 1 file",
			detail: {
				files: [
					{
						path: `backend/internal/${SUBJECTS[index % SUBJECTS.length]}/handler.go`,
						additions: 12 + index,
						deletions: index % 7,
					},
				],
			},
			createdAt: t(minute, 25),
		});

		items.push({
			kind: "message",
			id: `hr-${index}`,
			turnId,
			sequence: (sequence += 1),
			revision: 2,
			role: "assistant",
			origin: "provider",
			text:
				`Done. ${SUBJECTS[index % SUBJECTS.length]} now returns the durable snapshot instead of ` +
				`re-deriving it, and the handler is thinner by ${8 + index} lines.` +
				(index % 3 === 0
					? `\n\n\`\`\`go\nfunc (h *Handler) snapshot(ctx context.Context, id string) (*Conversation, error) {\n\treturn h.store.Snapshot(ctx, id)\n}\n\`\`\``
					: ""),
			streaming: false,
			createdAt: t(minute, 38),
		});
	}

	return {
		conversationId: "conv-ao-long",
		sessionId: "ao-long",
		harness: "codex",
		mode: "chat",
		controller: { state: "ready" },
		latestSequence: sequence,
		oldestSequence: 1,
		hasMoreBefore: false,
		settings: { model: "gpt-5.6-terra", reasoningEffort: "medium" },
		turns: conversationTurns,
		items,
	};
}

const QUESTIONS = [
	"Wire the snapshot endpoint into the handler",
	"Why is the conversation store re-deriving turn state?",
	"Move the approval fan-out behind the port",
	"Check the migration applies on an existing database",
];

const COMMANDS = [
	"rg -n 'Snapshot' backend/internal",
	"go build ./...",
	"git diff --stat",
	"sed -n '1,80p' backend/internal/domain/conversation.go",
	"go test ./internal/conversation/...",
];

const SUBJECTS = ["conversation", "session_manager", "httpd", "ports"];

/* -------------------------------------------------------------------------- */
/* provider state that is hard to produce on demand                            */
/* -------------------------------------------------------------------------- */

/**
 * A default Codex install: reasoning items arrive, and every one of them is empty.
 *
 * This is the case the reasoning toggle has to survive. The provider emits a
 * reasoning item per tool call whether or not summaries are configured, and writes a
 * body only when the user's own `~/.codex/config.toml` asks for one — measured on
 * codex-cli 0.146.0 as six empty items by default against ten items with nine bodies
 * once the setting was on. AO does not rewrite that file, so the UI has to explain
 * the emptiness rather than look broken.
 */
export const chatFixtureReasoningEmpty: ConversationSnapshot = {
	...chatFixtureSettled,
	conversationId: "conv-ao-16",
	sessionId: "ao-16",
	items: chatFixtureSettled.items.map((item) =>
		item.kind === "activity" && item.activityKind === "reasoning"
			? { ...item, detail: undefined }
			: item,
	),
};

/**
 * A tool server that failed to start, which the timeline cannot show.
 *
 * The agent will not mention the tools it does not have: it simply works around
 * them, so the user sees a worse answer with no cause. The reload control is offered
 * here because this is exactly the state it exists for.
 */
export const chatFixtureMcpFailed: ConversationSnapshot = {
	...chatFixture,
	conversationId: "conv-ao-17",
	sessionId: "ao-17",
	mcpServers: [
		{ name: "github", status: "ready" },
		{
			name: "playwright",
			status: "failed",
			failureReason: "startup_timeout",
			error: "npx @playwright/mcp@latest did not report ready within 30s",
		},
		{ name: "postgres", status: "cancelled", failureReason: "cancelled_by_client" },
	],
};

/**
 * The provider answered with a different model than the one that was chosen.
 *
 * Without this rendered, the composer keeps advertising `gpt-5.6-terra` while every
 * answer comes from something else — the surface quietly lies about what produced
 * the work.
 */
export const chatFixtureRerouted: ConversationSnapshot = {
	...chatFixtureSettled,
	conversationId: "conv-ao-18",
	sessionId: "ao-18",
	modelReroute: {
		fromModel: "gpt-5.6-terra",
		toModel: "gpt-5.6-terra-mini",
		reason: "The requested model is at capacity for this account tier",
		providerTurnId: "019fbdd1-fdac-76f2",
		at: t(39, 40),
	},
	items: [
		...chatFixtureSettled.items,
		{
			kind: "activity",
			id: "a-reroute-1",
			turnId: "turn-2",
			sequence: 15,
			revision: 0,
			activityKind: "system",
			status: "completed",
			summary: "Model rerouted",
			detail: {
				event: "model.rerouted",
				fromModel: "gpt-5.6-terra",
				toModel: "gpt-5.6-terra-mini",
				reason: "The requested model is at capacity for this account tier",
			},
			createdAt: t(39, 40),
		},
	],
	latestSequence: 15,
};

/**
 * The provider wants credentials the daemon does not hold.
 *
 * Nothing the user does inside AO will help, and every turn they send until they fix
 * it will fail — which is why this is the loudest thing the surface can say, and why
 * it names the command instead of telling them to re-authenticate.
 */
export const chatFixtureReauth: ConversationSnapshot = {
	...chatFixtureSettled,
	conversationId: "conv-ao-19",
	sessionId: "ao-19",
	account: {
		authMode: "chatgpt",
		planLabel: "Pro",
		reauthRequiredAt: t(39, 50),
		reauthReason: "The stored ChatGPT session expired and the refresh token was rejected.",
	},
	items: [
		...chatFixtureSettled.items,
		{
			kind: "activity",
			id: "a-reauth-1",
			sequence: 15,
			revision: 0,
			activityKind: "system",
			status: "failed",
			summary: "The provider needs you to sign in again",
			detail: {
				event: "auth.reauth_required",
				reason: "The stored ChatGPT session expired and the refresh token was rejected.",
			},
			createdAt: t(39, 50),
		},
	],
	latestSequence: 15,
};

/**
 * The provider's own thread is broken while AO's controller is fine.
 *
 * The combination is the point: the controller banner says nothing, because from
 * AO's side the connection is healthy. Only the provider knows the thread behind it
 * has faulted, and without this the session reads as an agent that went quiet.
 */
export const chatFixtureThreadError: ConversationSnapshot = {
	...chatFixtureSettled,
	conversationId: "conv-ao-20",
	sessionId: "ao-20",
	controller: { state: "ready" },
	threadState: {
		status: "system_error",
		waitingOn: ["user_input"],
	},
};

/** A third: an empty conversation, the first thing a new session shows. */
export const chatFixtureEmpty: ConversationSnapshot = {
	conversationId: "conv-ao-15",
	sessionId: "ao-15",
	harness: "codex",
	mode: "chat",
	controller: { state: "ready" },
	latestSequence: 0,
	oldestSequence: 1,
	hasMoreBefore: false,
	settings: {},
	turns: [],
	items: [],
};
