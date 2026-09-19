import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";
import { type ChangelogEntry, slugify } from "./changelog-utils";
import { normalizeContentDate } from "./content-utils";

export {
	type ChangelogEntry,
	formatChangelogDate,
	slugify,
} from "./changelog-utils";

const CHANGELOG_DIR = path.join(process.cwd(), "content/changelog");

// Releases are pulled from GitHub at build time, so the changelog refreshes
// whenever the landing is redeployed (a push to main, or a manual deploy).
// Curated MDX entries in content/changelog take precedence for the same version.
const RELEASES_REPO = "Untrivial-ai/agent-orchestrator";
// Only stable vMAJOR.MINOR.PATCH tags — skips nightlies, per-PR prereleases,
// and package tags like @composio/ao@x.
const STABLE_TAG = /^v\d+\.\d+\.\d+$/;

interface GithubRelease {
	tag_name: string;
	name: string | null;
	body: string | null;
	published_at: string;
	draft: boolean;
	prerelease: boolean;
}

function parseFrontmatter(filePath: string): ChangelogEntry | null {
	try {
		const fileContent = fs.readFileSync(filePath, "utf-8");
		const { data, content } = matter(fileContent);

		const slug = path.basename(filePath, ".mdx");
		const dateValue = normalizeContentDate(data.date) as string;

		return {
			slug,
			url: `/changelog/${slug}`,
			title: data.title ?? "Untitled",
			description: data.description,
			date: dateValue,
			image: data.image,
			content,
			source: "mdx",
			draft: data.draft === true,
			kind: data.kind === "weekly" ? "weekly" : undefined,
		};
	} catch {
		return null;
	}
}

function getMdxEntries(): ChangelogEntry[] {
	if (!fs.existsSync(CHANGELOG_DIR)) {
		return [];
	}

	const files = fs.readdirSync(CHANGELOG_DIR).filter((f) => f.endsWith(".mdx"));

	return files
		.map((file) => parseFrontmatter(path.join(CHANGELOG_DIR, file)))
		.filter((entry): entry is ChangelogEntry => entry !== null && !entry.draft);
}
function isStableReleaseWithBody(r: GithubRelease): boolean {
	return !r.draft && !r.prerelease && STABLE_TAG.test(r.tag_name) && (r.body ?? "").trim().length > 0;
}

// A release body is plain GitHub Markdown, kept verbatim and rendered through a
// Markdown-only path (source: "release") — it never touches the MDX compiler,
// so arbitrary text (import/export lines, JSX, {expressions}) can't break it.
function releaseToEntry(r: GithubRelease): ChangelogEntry {
	const slug = slugify(r.tag_name);
	return {
		slug,
		url: `/changelog/${slug}`,
		title: r.name?.trim() || r.tag_name,
		date: normalizeContentDate(r.published_at) as string,
		content: r.body ?? "",
		source: "release",
		draft: false,
	};
}

async function getReleaseEntries(): Promise<ChangelogEntry[]> {
	try {
		const token = process.env.GITHUB_TOKEN;
		const res = await fetch(`https://api.github.com/repos/${RELEASES_REPO}/releases?per_page=100`, {
			headers: {
				Accept: "application/vnd.github+json",
				"X-GitHub-Api-Version": "2022-11-28",
				...(token ? { Authorization: `Bearer ${token}` } : {}),
			},
			// Baked in at build for the static export; the redeploy is the refresh.
			next: { revalidate: 3600 },
		});
		if (!res.ok) {
			return [];
		}
		const releases = (await res.json()) as GithubRelease[];
		// Single pass: filter + map together (no double iteration).
		const entries: ChangelogEntry[] = [];
		for (const r of releases) {
			if (isStableReleaseWithBody(r)) entries.push(releaseToEntry(r));
		}
		return entries;
	} catch {
		return [];
	}
}

// The X.Y.Z inside a title/slug, used to dedupe a curated entry against its
// eventual GitHub release.
function versionKey(entry: ChangelogEntry): string | null {
	const match = `${entry.title} ${entry.slug}`.match(/(\d+)\.(\d+)\.(\d+)/);
	return match ? `${match[1]}.${match[2]}.${match[3]}` : null;
}

export async function getChangelogEntries(): Promise<ChangelogEntry[]> {
	const mdx = getMdxEntries();
	const releases = await getReleaseEntries();
	const curatedVersions = new Set(mdx.map(versionKey).filter((v): v is string => v !== null));

	const merged = [
		...mdx,
		...releases.filter((r) => {
			const v = versionKey(r);
			return v === null || !curatedVersions.has(v);
		}),
	];

	return merged.sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime());
}

export async function getChangelogEntry(slug: string): Promise<ChangelogEntry | undefined> {
	const entries = await getChangelogEntries();
	return entries.find((entry) => entry.slug === slug);
}

export async function getAllChangelogSlugs(): Promise<string[]> {
	const entries = await getChangelogEntries();
	return entries.map((entry) => entry.slug);
}

export function extractToc(
	content: string,
): { id: string; text: string; level: number }[] {
	const headingRegex = /^(#{2,3})\s+(.+)$/gm;
	const toc: { id: string; text: string; level: number }[] = [];

	for (const match of content.matchAll(headingRegex)) {
		const hashes = match[1];
		const heading = match[2];
		if (!hashes || !heading) continue;

		const level = hashes.length;
		const text = heading.trim();
		const id = slugify(text);

		toc.push({ id, text, level });
	}

	return toc;
}
