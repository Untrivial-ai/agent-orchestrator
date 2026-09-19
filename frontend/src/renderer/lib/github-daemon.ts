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

async function daemonFetch<T>(path: string, init?: RequestInit): Promise<T> {
	const baseUrl = getApiBaseUrl();
	if (!baseUrl) throw new Error("Daemon is not running.");
	const res = await fetch(`${baseUrl}${path}`, init);
	if (!res.ok) {
		const body = await res.json().catch(() => ({}));
		const message = typeof body?.message === "string" ? body.message : typeof body?.error === "string" ? body.error : `Request failed (${res.status})`;
		throw new Error(message);
	}
	return (await res.json()) as T;
}

export async function getGitHubStatus(): Promise<GitHubStatusResponse> {
	return daemonFetch<GitHubStatusResponse>("/api/v1/github/status");
}

export async function listGitHubRepos(): Promise<GitHubReposResponse> {
	return daemonFetch<GitHubReposResponse>("/api/v1/github/repos");
}

export async function saveGitHubPAT(pat: string): Promise<void> {
	await daemonFetch<{ status: string }>("/api/v1/github/pat", {
		method: "PUT",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ pat }),
	});
}
