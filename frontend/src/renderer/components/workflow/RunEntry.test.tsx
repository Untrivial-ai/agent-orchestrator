import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RunEntry } from "./RunEntry";

const baseRun = {
	id: "run-1",
	taskId: "task-1",
	attempt: 1,
	status: "succeeded",
	sessionId: "sess-abc",
	agentRoleId: "role-dev",
	providerId: "openai",
	providerDisplayName: "OpenAI",
	providerModelId: "gpt-4o",
	providerModelName: "GPT-4o",
	executorType: "claude-code",
	createdAt: "2026-09-12T10:00:00Z",
	startedAt: "2026-09-12T10:00:05Z",
	finishedAt: "2026-09-12T10:05:00Z",
	resultSummary: "All tests passed",
	errorMessage: "",
	previousRunId: "",
	retryMode: "",
};

describe("RunEntry", () => {
	it("renders attempt number", () => {
		render(<RunEntry run={baseRun as never} />);
		expect(screen.getByText(/#1/)).toBeInTheDocument();
	});

	it("renders status badge", () => {
		render(<RunEntry run={baseRun as never} />);
		expect(screen.getByText("Succeeded")).toBeInTheDocument();
	});

	it("renders provider display name", () => {
		render(<RunEntry run={baseRun as never} />);
		expect(screen.getByText("OpenAI")).toBeInTheDocument();
	});

	it("renders model name", () => {
		render(<RunEntry run={baseRun as never} />);
		expect(screen.getByText("GPT-4o")).toBeInTheDocument();
	});

	it("renders result summary", () => {
		render(<RunEntry run={baseRun as never} />);
		expect(screen.getByText("All tests passed")).toBeInTheDocument();
	});

	it("does not render retry lineage for a normal run", () => {
		render(<RunEntry run={baseRun as never} />);
		expect(screen.queryByText(/Previous Run/i)).not.toBeInTheDocument();
		expect(screen.queryByText(/Retry Mode/i)).not.toBeInTheDocument();
	});

	it("renders retry lineage when previousRunId is set", () => {
		const retryRun = { ...baseRun, previousRunId: "run-0", retryMode: "resume" };
		render(<RunEntry run={retryRun as never} />);
		expect(screen.getByText("run-0")).toBeInTheDocument();
		expect(screen.getByText(/resume/)).toBeInTheDocument();
	});

	it("renders error message when present", () => {
		const failedRun = { ...baseRun, status: "failed", errorMessage: "Timeout exceeded", resultSummary: "" };
		render(<RunEntry run={failedRun as never} />);
		expect(screen.getByText("Timeout exceeded")).toBeInTheDocument();
	});
});
