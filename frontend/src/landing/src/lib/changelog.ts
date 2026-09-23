import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";
import {
	getStableReleaseEntries,
	getStableReleaseEntry,
} from "./changelog-releases";
import { type ChangelogEntry, slugify } from "./changelog-utils";
import { normalizeContentDate } from "./content-utils";

export {
	type ChangelogEntry,
	formatChangelogDate,
	slugify,
} from "./changelog-utils";

const CHANGELOG_DIR = path.join(process.cwd(), "content/changelog");

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
		};
	} catch {
		return null;
	}
}

export function getWeeklyUpdates(): ChangelogEntry[] {
	return getLocalEntries((file) => file.endsWith("-weekly-update.mdx"));
}

function getLocalEntries(
	includeFile: (file: string) => boolean = () => true,
): ChangelogEntry[] {
	if (!fs.existsSync(CHANGELOG_DIR)) {
		return [];
	}

	const files = fs
		.readdirSync(CHANGELOG_DIR)
		.filter((file) => file.endsWith(".mdx") && includeFile(file));

	return files
		.map((file) => parseFrontmatter(path.join(CHANGELOG_DIR, file)))
		.filter((entry): entry is ChangelogEntry => entry !== null && !entry.draft)
		.sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime());
}

/** Curated weekly updates are the only entries shown on the changelog index. */
export function getChangelogEntries(): ChangelogEntry[] {
	return getWeeklyUpdates();
}

export async function getChangelogEntry(slug: string): Promise<ChangelogEntry | undefined> {
	const localEntry = getLocalEntries().find((entry) => entry.slug === slug);
	return localEntry ?? getStableReleaseEntry(slug);
}

export async function getAllChangelogSlugs(): Promise<string[]> {
	const localSlugs = getLocalEntries().map((entry) => entry.slug);
	const releaseSlugs = (await getStableReleaseEntries()).map(
		(entry) => entry.slug,
	);
	return [...new Set([...localSlugs, ...releaseSlugs])];
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
