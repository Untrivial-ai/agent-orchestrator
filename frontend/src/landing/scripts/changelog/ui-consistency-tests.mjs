import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { load } from "cheerio";

const changelogUrl = process.env.CHANGELOG_URL;
const changelogPath = "/changelog/";
const outputDirectory = path.resolve(process.env.CHANGELOG_OUT_DIR ?? "out");

function hasClasses(element, requiredClasses) {
	const classes = new Set((element.attr("class") ?? "").split(/\s+/).filter(Boolean));
	return requiredClasses.every((className) => classes.has(className));
}

async function loadPage(route) {
	if (changelogUrl) {
		const url = new URL(route, changelogUrl).toString();
		const response = await fetch(url);
		if (!response.ok) {
			throw new Error(`${url} returned HTTP ${response.status}`);
		}

		return load(await response.text());
	}

	const pathname = new URL(route, "http://localhost").pathname;
	const relativePath = decodeURIComponent(pathname).replace(/^\/+|\/+$/g, "");
	const htmlPath = path.join(outputDirectory, relativePath, "index.html");
	return load(await readFile(htmlPath, "utf8"));
}

test("every changelog feed entry uses the weekly UI contract", async () => {
	const $ = await loadPage(changelogPath);
	assert.ok(hasClasses($("main").first(), ["select-text"]), "the changelog page is not selectable");
	const feed = $("section[aria-label='Weekly product updates']");
	assert.ok(hasClasses(feed.parent(), ["max-w-4xl"]), "the changelog content is not max-w-4xl");
	const articles = feed.children("article").toArray();
	assert.ok(articles.length > 0, "the changelog feed rendered no entries");

	const failures = [];
	const seenIds = new Set();

	for (const articleNode of articles) {
		const article = $(articleNode);
		const id = article.attr("id") ?? "(missing id)";
		const title = article.children("a").first().find("h2").first();
		const titleText = title.text().trim() || "(missing title)";
		const description = article.children("p.text-lg").first();
		const body = article.children("div.prose").first();
		const headings = body
			.find("h2")
			.toArray()
			.map((heading) => $(heading).text().trim());
		const supportingHeadings = new Set(["Improvements", "Bug fixes", "Learn more", "Maintenance"]);
		const majorHeadings = headings.filter((heading) => !supportingHeadings.has(heading));
		const issues = [];

		if (seenIds.has(id)) issues.push("duplicate article id");
		seenIds.add(id);
		if (!id.startsWith("changelog-")) issues.push("missing stable changelog id");
		if (!hasClasses(article, ["relative", "select-text"]) || article.hasClass("border-b")) {
			issues.push("article layout classes differ");
		}
		if (!hasClasses(title, ["text-2xl", "md:text-3xl", "font-medium", "mb-4"])) {
			issues.push("title typography classes differ");
		}
		if (!description.length) issues.push("missing description");
		if (
			description.length &&
			!hasClasses(description, ["text-lg", "text-muted-foreground", "mb-6"])
		) {
			issues.push("description typography classes differ");
		}
		if (!body.length) issues.push("missing changelog body");
		if (body.length && !hasClasses(body, ["prose", "prose-invert", "max-w-none"])) {
			issues.push("body typography classes differ");
		}
		if (!headings.includes("Learn more")) issues.push("missing Learn more section");
		if (headings[0] === "Highlights") issues.push("release-note structure appears in weekly feed");
		if (!headings.includes("Maintenance") && (majorHeadings.length < 1 || majorHeadings.length > 4)) {
			issues.push(`expected 1-4 major features, found ${majorHeadings.length}`);
		}
		if (body.find("img").length > 1) issues.push("more than one product image");
		if (body.find("hr").length > 0) issues.push("contains a horizontal divider");
		for (const prLink of body.find("a[href*='/pull/']").toArray()) {
			const link = $(prLink);
			if (!link.hasClass("underline") || /(?:^|\s)bg-/.test(link.attr("class") ?? "")) {
				issues.push("pull request link is not plain underlined text");
			}
		}
		if (!body.find("a[href='/docs']").length) issues.push("missing documentation link");
		if (!body.find("a[href*='github.com/Untrivial-ai/agent-orchestrator/releases']").length) {
			issues.push("missing release archive link");
		}

		if (issues.length > 0) failures.push({ id, title: titleText, headings, issues });
	}

	assert.deepEqual(failures, []);
});

test("every weekly detail page keeps all changelog text selectable", async () => {
	const feed = await loadPage(changelogPath);
	const weeklyUrls = feed("section[aria-label='Weekly product updates'] article > a")
		.toArray()
		.map((link) => feed(link).attr("href"))
		.filter(Boolean);

	assert.ok(weeklyUrls.length > 0, "the changelog feed contained no weekly detail links");

	const failures = [];
	for (const url of weeklyUrls) {
		const detail = await loadPage(url);
		const main = detail("main").first();
		const article = main.find("article").first();
		const title = article.find("h2").first().text().trim() || url;
		const issues = [];

		if (!hasClasses(main, ["select-text"])) issues.push("page is not selectable");
		if (!hasClasses(article, ["select-text"])) issues.push("entry is not selectable");
		if (!article.children("p.text-lg").length) issues.push("description is missing");
		if (!article.children("div.prose").length) issues.push("body is missing");

		if (issues.length > 0) failures.push({ url, title, issues });
	}

	assert.deepEqual(failures, []);
});
