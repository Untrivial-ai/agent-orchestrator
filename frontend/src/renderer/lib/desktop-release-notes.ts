const RELEASE_ATTRIBUTION_PATTERN =
	/\s+by\s+@[A-Za-z0-9-]+(?:\[bot\])?\s+in\s+(\[#\d+\]\(https:\/\/github\.com\/Untrivial-ai\/agent-orchestrator\/pull\/\d+\))\s*$/;
const FULL_CHANGELOG_PATTERN =
	/^\*\*Full Changelog\*\*:\s+https:\/\/github\.com\/Untrivial-ai\/agent-orchestrator\/(?:compare|commits)\/\S+\s*$/i;

export function prepareDesktopReleaseNotes(notes: string): string {
	const lines = notes.split(/\r?\n/).flatMap((line) => {
		if (FULL_CHANGELOG_PATTERN.test(line.trim())) {
			return [];
		}
		return line.replace(RELEASE_ATTRIBUTION_PATTERN, " $1").trimEnd();
	});

	return lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}
