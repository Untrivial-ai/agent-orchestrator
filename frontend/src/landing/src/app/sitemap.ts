import { COMPANY } from "@ao/shared/constants";
import type { MetadataRoute } from "next";
import { getBlogPosts } from "@/lib/blog";
import { getChangelogEntries } from "@/lib/changelog";
import { getComparisonPages } from "@/lib/compare";
import { getAllDocSlugs } from "@/lib/docs";
import { getAllLegalSlugs, getLegalPage } from "@/lib/legal";
import { getAllPeople } from "@/lib/people";

export const dynamic = "force-static";

export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
	const baseUrl = COMPANY.MARKETING_URL;

	const staticPages: MetadataRoute.Sitemap = [
		{
			url: baseUrl,
			lastModified: new Date(),
			changeFrequency: "weekly",
			priority: 1.0,
		},
		{
			url: `${baseUrl}/download/`,
			lastModified: new Date(),
			changeFrequency: "weekly",
			priority: 0.9,
		},
		{
			url: `${baseUrl}/design-partners/`,
			lastModified: new Date(),
			changeFrequency: "monthly",
			priority: 0.8,
		},
		{
			url: `${baseUrl}/hackathons/`,
			lastModified: new Date(),
			changeFrequency: "monthly",
			priority: 0.8,
		},
		{
			url: `${baseUrl}/hackathons/syndicate/`,
			lastModified: new Date(),
			changeFrequency: "monthly",
			priority: 0.8,
		},
		{
			url: `${baseUrl}/testimonials/`,
			lastModified: new Date(),
			changeFrequency: "monthly",
			priority: 0.7,
		},
		{
			url: `${baseUrl}/privacy/`,
			lastModified: new Date("2026-07-30"),
			changeFrequency: "yearly",
			priority: 0.3,
		},
		{
			url: `${baseUrl}/blog/`,
			lastModified: new Date(),
			changeFrequency: "daily",
			priority: 0.9,
		},
		{
			url: `${baseUrl}/changelog/`,
			lastModified: new Date(),
			changeFrequency: "weekly",
			priority: 0.9,
		},
		{
			url: `${baseUrl}/llms.txt`,
			lastModified: new Date(),
			changeFrequency: "weekly",
			priority: 0.3,
		},
	];

	const posts = getBlogPosts();
	const blogPages: MetadataRoute.Sitemap = posts.map((post) => ({
		url: `${baseUrl}/blog/${post.slug}/`,
		lastModified: new Date(post.date),
		changeFrequency: "monthly" as const,
		priority: 0.8,
	}));

	const changelogEntries = await getChangelogEntries();
	const changelogPages: MetadataRoute.Sitemap = changelogEntries.map(
		(entry) => ({
			url: `${baseUrl}/changelog/${entry.slug}/`,
			lastModified: new Date(entry.date),
			changeFrequency: "monthly" as const,
			priority: 0.8,
		}),
	);

	const people = getAllPeople();
	const teamPages: MetadataRoute.Sitemap = people.map((person) => ({
		url: `${baseUrl}/team/${person.id}/`,
		lastModified: new Date(),
		changeFrequency: "monthly" as const,
		priority: 0.7,
	}));

	const comparisonPages: MetadataRoute.Sitemap = getComparisonPages().map(
		(page) => ({
			url: `${baseUrl}/compare/${page.slug}/`,
			lastModified: new Date(page.lastUpdated || page.date),
			changeFrequency: "weekly" as const,
			priority: 0.9,
		}),
	);

	const legalPages: MetadataRoute.Sitemap = getAllLegalSlugs().map((slug) => {
		const page = getLegalPage(slug);
		return {
			url: `${baseUrl}/${slug}/`,
			lastModified: page?.lastUpdated ? new Date(page.lastUpdated) : new Date(),
			changeFrequency: "yearly" as const,
			priority: 0.3,
		};
	});

	const docsPages: MetadataRoute.Sitemap = getAllDocSlugs().map((slug) => ({
		url: `${baseUrl}/docs${slug.length ? `/${slug.join("/")}` : ""}/`,
		lastModified: new Date(),
		changeFrequency: "weekly" as const,
		priority: 0.7,
	}));

	return [
		...staticPages,
		...docsPages,
		...blogPages,
		...changelogPages,
		...teamPages,
		...comparisonPages,
		...legalPages,
	];
}
