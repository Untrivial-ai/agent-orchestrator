import { describe, expect, it } from "vitest";
import { sessionsAtRiskFromInstall, type UpdateRiskSession } from "./update-install-risk";

function session(overrides: Partial<UpdateRiskSession> = {}): UpdateRiskSession {
	return {
		id: "s1",
		title: "Session",
		workspaceName: "repo",
		provider: "claude-code",
		mode: "chat",
		status: "working",
		...overrides,
	};
}

describe("sessionsAtRiskFromInstall", () => {
	it("flags a working chat session on a daemon-owned driver", () => {
		expect(sessionsAtRiskFromInstall([session()])).toHaveLength(1);
	});

	it("treats no_signal as possibly mid-turn", () => {
		expect(sessionsAtRiskFromInstall([session({ status: "no_signal" })])).toHaveLength(1);
	});

	it("flags a chat turn paused for approval or input", () => {
		expect(sessionsAtRiskFromInstall([session({ status: "needs_input" })])).toHaveLength(1);
	});

	it("spares TUI sessions, whose runtime is detached and re-adopted", () => {
		expect(sessionsAtRiskFromInstall([session({ mode: "tui" })])).toEqual([]);
	});

	it.each(["codex", "claude-code", "cursor", "opencode", "droid", "kimi", "kimchi", "pi", "omp"] as const)(
		"spares %s only when the daemon confirms persistent ownership",
		(provider) => {
			expect(sessionsAtRiskFromInstall([session({ provider, chatProviderPreserved: true })])).toEqual([]);
			expect(sessionsAtRiskFromInstall([session({ provider })])).toHaveLength(1);
			expect(sessionsAtRiskFromInstall([session({ provider, chatProviderPreserved: false })])).toHaveLength(1);
		},
	);

	it("spares sessions with no turn in flight", () => {
		for (const status of ["idle", "exited", "merged", "pr_open"]) {
			expect(sessionsAtRiskFromInstall([session({ status })])).toEqual([]);
		}
	});

	it("spares terminated sessions", () => {
		expect(sessionsAtRiskFromInstall([session({ isTerminated: true })])).toEqual([]);
	});
});
