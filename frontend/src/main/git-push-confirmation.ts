import type { MessageBoxOptions } from "electron";
import type { GitPushProposal } from "../shared/git-push";

function firstCommitMessage(proposal: GitPushProposal): string {
	const first = proposal.commits[0]?.trim();
	if (!first) return "(commit message unavailable)";
	const separator = first.indexOf(" ");
	return separator === -1 ? first : first.slice(separator + 1);
}

export function buildGitPushConfirmationOptions(proposal: GitPushProposal): MessageBoxOptions {
	const shortSha = proposal.headSha.slice(0, 12);
	const target = `${proposal.remoteUrl}  refs/heads/${proposal.branch}`;
	return {
		type: "warning",
		title: "Confirm Git Push",
		message: `Push commit ${shortSha}?`,
		detail: [
			`Project: ${proposal.projectName || proposal.projectId}`,
			`Repository: ${proposal.repository}`,
			`Remote: ${proposal.remote}`,
			`Remote URL: ${proposal.remoteUrl}`,
			`Branch: ${proposal.branch}`,
			`Current HEAD: ${proposal.headSha}`,
			`Commit: ${shortSha} ${firstCommitMessage(proposal)}`,
			`Push target: ${target}`,
			`Changed files: ${proposal.changedFiles.length}`,
		].join("\n"),
		buttons: ["Cancel", "Confirm Push"],
		defaultId: 0,
		cancelId: 0,
		noLink: true,
	};
}
