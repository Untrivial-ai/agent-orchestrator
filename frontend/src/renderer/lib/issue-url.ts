/**
 * Turn a session issue id into a forge URL when the repository is unambiguous.
 *
 * Intake stamps `github:owner/repo#12` / `gitlab:group/repo#7[@host]`. Those
 * already name the repo, so they link without consulting the project. A bare
 * number (`42`, `github:42`) only links when the caller supplies exactly one
 * project origin — never by guessing from a PR or a multi-repo workspace.
 */

export type TrackerIssueLink = {
	url: string;
	label: string;
	nativeId: string;
};

export type ProjectIssueOrigin = {
	/** HTTPS or SSH origin for the project's single repository. */
	originUrl?: string;
};

type CanonicalIssue = {
	provider: "github" | "gitlab";
	repo: string;
	number: number;
	host?: string;
};

const PROVIDER_PREFIX = /^(github|gitlab):/i;

export function trackerIssueLink(
	issueId?: string,
	project?: ProjectIssueOrigin,
): TrackerIssueLink | undefined {
	const raw = issueId?.trim();
	if (!raw) return undefined;

	if (/^https?:\/\//i.test(raw)) {
		return linkFromWebUrl(raw);
	}

	const canonical = parseCanonicalIssueId(raw);
	if (canonical) return toLink(canonical);

	// Future-proofing: board/topbar currently pass only `issueId`, so this
	// origin-backed path is unused in production. Keep it so a later caller
	// can supply a single project origin without guessing from PRs.
	const number = parseBareIssueNumber(raw);
	if (number === undefined) return undefined;
	const fromOrigin = parseOrigin(project?.originUrl);
	if (!fromOrigin) return undefined;
	return toLink({ ...fromOrigin, number });
}

function toLink(issue: CanonicalIssue): TrackerIssueLink | undefined {
	const url = issueUrl(issue);
	if (!url) return undefined;
	return {
		url,
		label: `#${issue.number}`,
		nativeId: `${issue.repo}#${issue.number}`,
	};
}

function parseCanonicalIssueId(raw: string): CanonicalIssue | undefined {
	const match = /^(github|gitlab):(.+)#(\d+)(?:@(.+))?$/i.exec(raw);
	if (!match) return undefined;
	const provider = match[1].toLowerCase() as CanonicalIssue["provider"];
	const repo = normalizeRepoPath(match[2]);
	const number = Number(match[3]);
	const host = match[4]?.trim();
	if (!Number.isInteger(number) || number <= 0 || !isPlausibleRepo(provider, repo)) return undefined;
	return { provider, repo, number, host: host || undefined };
}

function parseBareIssueNumber(raw: string): number | undefined {
	const stripped = raw.replace(PROVIDER_PREFIX, "").replace(/^#/, "");
	if (!/^\d+$/.test(stripped)) return undefined;
	const number = Number(stripped);
	return Number.isInteger(number) && number > 0 ? number : undefined;
}

function linkFromWebUrl(raw: string): TrackerIssueLink | undefined {
	let url: URL;
	try {
		url = new URL(raw);
	} catch {
		return undefined;
	}
	if (url.protocol !== "http:" && url.protocol !== "https:") return undefined;

	const gitlab = parseGitLabIssuePath(url.pathname);
	if (gitlab) {
		return toLink({
			provider: "gitlab",
			repo: gitlab.repo,
			number: gitlab.number,
			host: gitlabHost(url.host),
		});
	}

	const github = parseGitHubIssuePath(url.pathname);
	if (!github || !isGitHubHost(url.hostname)) return undefined;
	return toLink({ provider: "github", repo: github.repo, number: github.number });
}

function parseOrigin(originUrl?: string): Omit<CanonicalIssue, "number"> | undefined {
	const raw = originUrl?.trim();
	if (!raw) return undefined;
	const normalized = sshToHttps(raw);
	let url: URL;
	try {
		url = new URL(normalized);
	} catch {
		return undefined;
	}
	const repoPath = normalizeRepoPath(url.pathname);
	if (!repoPath) return undefined;
	if (isGitHubHost(url.hostname)) {
		if (!isPlausibleRepo("github", repoPath)) return undefined;
		return { provider: "github", repo: repoPath };
	}
	if (!isPlausibleRepo("gitlab", repoPath)) return undefined;
	return { provider: "gitlab", repo: repoPath, host: gitlabHost(url.host) };
}

function issueUrl(issue: CanonicalIssue): string | undefined {
	if (issue.provider === "github") {
		const host = issue.host?.trim() || "github.com";
		return `https://${host}/${issue.repo}/issues/${issue.number}`;
	}
	const host = issue.host?.trim() || "gitlab.com";
	return `https://${host}/${issue.repo}/-/issues/${issue.number}`;
}

function parseGitHubIssuePath(pathname: string): { repo: string; number: number } | undefined {
	const match = /^\/([^/]+\/[^/]+)\/issues\/(\d+)\/?$/.exec(pathname);
	if (!match) return undefined;
	const number = Number(match[2]);
	const repo = normalizeRepoPath(match[1]);
	if (!Number.isInteger(number) || number <= 0 || !isPlausibleRepo("github", repo)) return undefined;
	return { repo, number };
}

function parseGitLabIssuePath(pathname: string): { repo: string; number: number } | undefined {
	const idx = pathname.indexOf("/-/issues/");
	if (idx <= 0) return undefined;
	const repo = normalizeRepoPath(pathname.slice(0, idx));
	const rest = pathname.slice(idx + "/-/issues/".length).replace(/\/$/, "");
	if (!/^\d+$/.test(rest)) return undefined;
	const number = Number(rest);
	if (!Number.isInteger(number) || number <= 0 || !isPlausibleRepo("gitlab", repo)) return undefined;
	return { repo, number };
}

function isPlausibleRepo(provider: CanonicalIssue["provider"], repo: string): boolean {
	const parts = repo.split("/");
	if (parts.some((part) => part === "")) return false;
	if (provider === "github") return parts.length === 2;
	return parts.length >= 2;
}

function normalizeRepoPath(path: string): string {
	const parts = path
		.split("/")
		.map((part) => part.trim())
		.filter(Boolean);
	if (parts.length === 0) return "";
	parts[parts.length - 1] = parts[parts.length - 1].replace(/\.git$/i, "");
	return parts.join("/");
}

function sshToHttps(raw: string): string {
	if (!raw.startsWith("git@")) return raw;
	const cut = raw.indexOf(":");
	if (cut <= 4) return raw;
	return `https://${raw.slice(4, cut)}/${raw.slice(cut + 1)}`;
}

function isGitHubHost(hostname: string): boolean {
	const host = hostname.toLowerCase().replace(/^www\./, "");
	return host === "github.com" || host.endsWith(".github.com") || host.endsWith(".ghe.io");
}

function gitlabHost(host: string): string | undefined {
	const normalized = host.toLowerCase();
	if (normalized === "gitlab.com" || normalized === "www.gitlab.com") return undefined;
	return host;
}
