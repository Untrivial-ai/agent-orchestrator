const DEFAULT_BRANCH_UNRESOLVED_CODE = "DEFAULT_BRANCH_UNRESOLVED";

function extractRemoteUrl(message: string): string | null {
	const singleQuoted = message.match(/fatal:\s+repository\s+'([^']+)'/i);
	if (singleQuoted?.[1]) return singleQuoted[1].replace(/\/+$/, "");
	const doubleQuoted = message.match(/repository\s+"([^"]+\.git)"\s+not found/i);
	if (doubleQuoted?.[1]) return doubleQuoted[1].replace(/\/+$/, "");
	return null;
}

function extractWorkspaceRepoName(message: string): string | null {
	const namedRepo = message.match(/resolve workspace repo\s+"([^"]+)"/i);
	if (namedRepo?.[1]) return namedRepo[1];
	return null;
}

export function formatOrchestratorStartupError(message: string): string {
	if (!message.includes(DEFAULT_BRANCH_UNRESOLVED_CODE)) return message;
	const repoName = extractWorkspaceRepoName(message);
	const remoteUrl = extractRemoteUrl(message);
	const isChild = repoName !== null && repoName !== "__root__";
	const repoLabel = isChild ? `child repository "${repoName}"` : repoName ? "workspace root repository" : "project repository";
	if (remoteUrl) {
		return `Project added, but orchestrator did not start. The ${repoLabel} still needs its remote repository set up at ${remoteUrl}. Create or fix that remote, then retry starting the orchestrator.`;
	}
	const guidance = isChild
		? "Check this child's default branch and remote HEAD configuration, then retry starting the orchestrator."
		: "Set its default branch in Project Settings, or check its remote configuration, then retry starting the orchestrator.";
	return `Project added, but orchestrator did not start. AO could not determine the default branch for the ${repoLabel}. ${guidance}\n\nDetails: ${message}`;
}
