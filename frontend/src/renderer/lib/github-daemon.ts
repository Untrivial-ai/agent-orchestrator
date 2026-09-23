import { getApiBaseUrl } from "./api-client";

export interface GitHubStatusResponse {
	connected: boolean;
}

export interface GitHubRepo {
	name: string;
	full_name: string;
	private: boolean;
	default_branch: string;
	clone_url: string;
}

export interface GitHubReposResponse {
	repos: GitHubRepo[];
}

export class GitHubDaemonError extends Error {
	constructor(
		message: string,
		readonly status: number,
		readonly code?: string,
	) {
		super(message);
		this.name = "GitHubDaemonError";
	}
}

export function isGitHubAuthInvalidError(error: unknown): boolean {
	return error instanceof GitHubDaemonError && error.status === 401 && error.code === "GITHUB_AUTH_INVALID";
}

async function daemonFetch<T>(path: string, init?: RequestInit): Promise<T> {
	const baseUrl = getApiBaseUrl();
	if (!baseUrl) throw new Error("Daemon is not running.");
	const res = await fetch(`${baseUrl}${path}`, init);
	if (!res.ok) {
		const body = await res.json().catch(() => ({}));
		const message = typeof body?.message === "string" ? body.message : typeof body?.error === "string" ? body.error : `Request failed (${res.status})`;
		const code = typeof body?.code === "string" ? body.code : undefined;
		throw new GitHubDaemonError(message, res.status, code);
	}
	return (await res.json()) as T;
}

export async function getGitHubStatus(): Promise<GitHubStatusResponse> {
	return daemonFetch<GitHubStatusResponse>("/api/v1/github/status");
}

export async function listGitHubRepos(): Promise<GitHubReposResponse> {
	return daemonFetch<GitHubReposResponse>("/api/v1/github/repos");
}

export interface GitHubOAuthRefresh {
	refreshToken?: string;
	expiresIn?: number;
	refreshTokenExpiresIn?: number;
}

// saveGitHubPAT stores a token on the daemon. For an expiring GitHub App OAuth
// token, pass the refresh material so the daemon can renew it transparently
// instead of forcing a reconnect; a plain PAT passes only the token.
export async function saveGitHubPAT(pat: string, oauth?: GitHubOAuthRefresh): Promise<void> {
	await daemonFetch<{ status: string }>("/api/v1/github/pat", {
		method: "PUT",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({
			pat,
			...(oauth?.refreshToken ? { refreshToken: oauth.refreshToken } : {}),
			...(oauth?.expiresIn ? { expiresIn: oauth.expiresIn } : {}),
			...(oauth?.refreshTokenExpiresIn ? { refreshTokenExpiresIn: oauth.refreshTokenExpiresIn } : {}),
		}),
	});
}
