import { describe, expect, it } from "vitest";
import {
	encodeCloudBrowserOrigin,
	isCloudBrowserProxyUrl,
	toCloudBrowserProxyUrl,
} from "./cloud-browser-proxy";
import {
	cloudDiffToWorkspaceDiffsResponse,
	cloudDiffToWorkspaceFilesResponse,
	cloudPullRequestToSessionSummary,
} from "./cloud-inspector-adapters";
import type { CloudCpPullRequestSummary, CloudCpWorkspaceDiff } from "./cloud-cp";

describe("cloud browser proxy URLs", () => {
	it("encodes origins the same way as the control-plane rewrite", () => {
		expect(encodeCloudBrowserOrigin("http://localhost:5173")).toBe(
			btoa("http://localhost:5173").replace(/=+$/, ""),
		);
	});

	it("rewrites sandbox http URLs onto the CP browser proxy", () => {
		const url = toCloudBrowserProxyUrl(
			"https://cloud.example.test",
			"org-1",
			"sess-1",
			"http://localhost:5173/app?x=1#hash",
		);
		expect(url).toBe(
			`https://cloud.example.test/api/cloud/v1/orgs/org-1/sessions/sess-1/browser/${encodeCloudBrowserOrigin("http://localhost:5173")}/app?x=1#hash`,
		);
		expect(isCloudBrowserProxyUrl(url, "org-1", "sess-1")).toBe(true);
	});

	it("leaves non-http URLs alone", () => {
		expect(toCloudBrowserProxyUrl("https://cloud.example.test", "o", "s", "about:blank")).toBe("about:blank");
	});
});

describe("cloud inspector adapters", () => {
	const diff: CloudCpWorkspaceDiff = {
		status: " M src/a.ts\n?? README.md\n",
		unstaged: "diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\n",
		staged: "",
		combined: "diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\n",
		diffBaseRef: "HEAD",
		diffBaseSha: "abc123",
		files: [
			{ path: "src/a.ts", status: "modified", additions: 1, deletions: 1, binary: false },
			{ path: "README.md", status: "untracked", additions: 0, deletions: 0, binary: false },
		],
		untrackedFiles: ["README.md"],
		truncated: { combined: false, stats: false },
	};

	it("maps workspace.diff into Files tab sections", () => {
		const response = cloudDiffToWorkspaceFilesResponse("sess-1", diff);
		expect(response.files).toHaveLength(2);
		expect(response.sections.untracked.map((file) => file.path)).toEqual(["README.md"]);
		expect(response.sections.unstaged.map((file) => file.path)).toEqual(["src/a.ts"]);
		expect(response.workspaceVersion).toBe("abc123");
	});

	it("serves the worker patch as one diffs group", () => {
		const response = cloudDiffToWorkspaceDiffsResponse("sess-1", diff, "combined", ["src/a.ts"]);
		expect(response.groups).toHaveLength(1);
		expect(response.groups[0]?.patch).toContain("src/a.ts");
		expect(response.groups[0]?.includedPaths).toEqual(["src/a.ts"]);
	});

	it("maps pull-request summaries into SessionPRSummary", () => {
		const pr: CloudCpPullRequestSummary = {
			url: "https://github.com/acme/app/pull/9",
			number: 9,
			title: "Ship it",
			state: "open",
			provider: "github",
			repository: "acme/app",
			author: "ada",
			sourceBranch: "feat",
			targetBranch: "main",
			headSha: "deadbeef",
			additions: 3,
			deletions: 1,
			changedFiles: 2,
			ci: { state: "passing", failingChecks: [] },
			review: { decision: "none", hasUnresolvedHumanComments: false, unresolvedBy: [], reviews: [] },
			mergeability: { state: "mergeable", reasons: [], pullRequestUrl: "https://github.com/acme/app/pull/9", conflictFiles: [] },
			updatedAt: "2026-01-01T00:00:00Z",
			observedAt: "2026-01-01T00:00:00Z",
			ciObservedAt: "2026-01-01T00:00:00Z",
			reviewObservedAt: "2026-01-01T00:00:00Z",
		};
		const summary = cloudPullRequestToSessionSummary(pr);
		expect(summary.number).toBe(9);
		expect(summary.repo).toBe("acme/app");
		expect(summary.ci.state).toBe("passing");
		expect(summary.mergeability.prUrl).toBe(pr.url);
	});
});
