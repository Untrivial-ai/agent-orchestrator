import { COMPANY, GITHUB_STARS_URL } from "@ao/shared/constants";
import { env } from "@/env";

export interface GitHubRepoStats {
  stars: number;
  forks: number;
  createdAt: string | null;
}

interface GitHubRepoResponse {
  stargazers_count?: number;
  forks_count?: number;
  created_at?: string;
}

function getGitHubApiUrl(): string | null {
  if (GITHUB_STARS_URL) return GITHUB_STARS_URL;

  const match = COMPANY.GITHUB_URL.match(/github\.com\/([^/]+\/[^/]+)/);
  return match ? `https://api.github.com/repos/${match[1]}` : null;
}

/** Compact hero-style count: 8600 → "8.6k". */
export function formatCompactCount(count: number): string {
  if (count >= 1000) {
    return `${(count / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  }
  return count.toString();
}

/** Marketing stat style: 8642 → "8,600+". */
export function formatStatPlus(count: number): string {
  if (count >= 100) {
    const rounded = Math.floor(count / 100) * 100;
    return `${rounded.toLocaleString("en-US")}+`;
  }
  return count.toLocaleString("en-US");
}

/** Whole months since the repo was created (min 1). */
export function monthsSince(createdAt: string | null, now = Date.now()): number {
  if (!createdAt) return 1;
  const created = Date.parse(createdAt);
  if (Number.isNaN(created)) return 1;
  const msPerMonth = 30.44 * 24 * 60 * 60 * 1000;
  return Math.max(1, Math.round((now - created) / msPerMonth));
}

export async function getGitHubRepoStats(): Promise<GitHubRepoStats | null> {
  const apiUrl = getGitHubApiUrl();
  if (!apiUrl) return null;

  try {
    const token = env.GITHUB_TOKEN?.trim();
    const response = await fetch(apiUrl, {
      headers: {
        Accept: "application/vnd.github.v3+json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      next: { revalidate: 3600 },
    });

    if (!response.ok) return null;

    const data = (await response.json()) as GitHubRepoResponse;
    if (
      typeof data.stargazers_count !== "number" ||
      typeof data.forks_count !== "number"
    ) {
      return null;
    }

    return {
      stars: data.stargazers_count,
      forks: data.forks_count,
      createdAt: typeof data.created_at === "string" ? data.created_at : null,
    };
  } catch {
    return null;
  }
}
