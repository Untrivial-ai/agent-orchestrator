import { describe, expect, it } from "vitest";
import { buildGitPushConfirmationOptions } from "./git-push-confirmation";
import type { GitPushProposal } from "../shared/git-push";

const proposal: GitPushProposal = {
	approvalId: "approval-1",
	projectId: "project-1",
	projectName: "My Project",
	repository: "D:\\work\\repo",
	remote: "origin",
	remoteUrl: "https://github.com/acme/repo.git",
	branch: "feature/safe-push",
	headSha: "0123456789abcdef0123456789abcdef01234567",
	commits: ["0123456 finish safe push"],
	diffSummary: "1 file changed",
	changedFiles: ["src/push.ts"],
	expiresAt: "2026-08-26T12:00:00Z",
};

describe("Git push native confirmation", () => {
	it("defaults to cancel and shows the exact approved Git state", () => {
		const options = buildGitPushConfirmationOptions(proposal);
		expect(options.defaultId).toBe(0);
		expect(options.cancelId).toBe(0);
		expect(options.buttons).toEqual(["Cancel", "Confirm Push"]);
		for (const value of [
			"My Project",
			"D:\\work\\repo",
			"origin",
			"https://github.com/acme/repo.git",
			"feature/safe-push",
			proposal.headSha,
			"finish safe push",
			"refs/heads/feature/safe-push",
		]) {
			expect(options.detail).toContain(value);
		}
	});
});
