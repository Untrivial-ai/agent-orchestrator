import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";

const root = process.cwd();
const changelogDirectory = path.join(root, "content/changelog");
const publicDirectory = path.join(root, "public");
const errors = [];
const ranges = [];

function dateOnly(value) {
	if (value instanceof Date) return value.toISOString().slice(0, 10);
	return String(value ?? "").slice(0, 10);
}

function nextDate(value) {
	const date = new Date(`${value}T00:00:00Z`);
	date.setUTCDate(date.getUTCDate() + 1);
	return date.toISOString().slice(0, 10);
}

for (const file of fs.readdirSync(changelogDirectory).filter((name) => name.endsWith(".mdx"))) {
	const filePath = path.join(changelogDirectory, file);
	const raw = fs.readFileSync(filePath, "utf8");
	const { data } = matter(raw);

	for (const property of ["title", "description", "date"]) {
		if (!data[property]) errors.push(`${file}: missing ${property} frontmatter`);
	}

	if (data.date && Number.isNaN(new Date(data.date).getTime())) {
		errors.push(`${file}: invalid date frontmatter`);
	}

	if (data.image) {
		const imagePath = path.join(publicDirectory, String(data.image).replace(/^\//, ""));
		if (!fs.existsSync(imagePath)) errors.push(`${file}: image does not exist: ${data.image}`);
	}

	if (data.rangeStart || data.rangeEnd) {
		const start = dateOnly(data.rangeStart);
		const end = dateOnly(data.rangeEnd);
		if (!/^\d{4}-\d{2}-\d{2}$/.test(start) || !/^\d{4}-\d{2}-\d{2}$/.test(end)) {
			errors.push(`${file}: rangeStart and rangeEnd must both be YYYY-MM-DD dates`);
		} else if (start > end) {
			errors.push(`${file}: rangeStart must not be after rangeEnd`);
		} else if (file.endsWith("-weekly-update.mdx")) {
			ranges.push({ file, start, end });
		}
	}

	if (file.endsWith("-weekly-update.mdx")) {
		if (/github\.com\/[^/]+\/[^/]+\/commit\/[a-f0-9]+/i.test(raw)) {
			errors.push(`${file}: raw commit links are not allowed in the public changelog`);
		}

		const headings = [...raw.matchAll(/^## (.+)$/gm)];
		const supportingSections = new Set(["Improvements", "Bug fixes", "Learn more", "Maintenance"]);
		const featureHeadings = headings.filter(
			(match) => !supportingSections.has(match[1].replace(/\s*<PRBadge.*$/, "").trim()),
		);
		if (featureHeadings.length > 4) {
			errors.push(`${file}: contains more than four major feature sections`);
		}
		if (!headings.some((match) => match[1].trim() === "Learn more")) {
			errors.push(`${file}: missing Learn more section`);
		}

		const images = [...raw.matchAll(/^!\[([^\]]+)\]\(([^)]+)\)\s*$/gm)];
		if (images.length > 1) {
			errors.push(`${file}: weekly entries may contain at most one product image`);
		}
		const firstSupportingSection = headings
			.filter((match) => supportingSections.has(match[1].trim()))
			.map((match) => match.index)
			.filter((index) => index !== undefined)
			.sort((a, b) => a - b)[0];
		if (
			firstSupportingSection !== undefined &&
			images.some((match) => match.index !== undefined && match.index > firstSupportingSection)
		) {
			errors.push(
				`${file}: product media must support a major feature before the Improvements section`,
			);
		}

		const references = [
			...raw.matchAll(/github\.com\/[^/]+\/[^/]+\/pull\/(\d+)/gm),
		].map((match) => Number(match[1]));
		const uniqueReferences = new Set(references);
		if (references.length !== uniqueReferences.size) {
			errors.push(`${file}: contains a duplicate pull request reference`);
		}
	}
}

ranges.sort((a, b) => a.start.localeCompare(b.start));
if (ranges.length > 0 && ranges[0].start !== "2026-02-13") {
	errors.push(`weekly history starts at ${ranges[0].start}; expected first commit date 2026-02-13`);
}
for (let index = 1; index < ranges.length; index += 1) {
	const previous = ranges[index - 1];
	const current = ranges[index];
	const expected = nextDate(previous.end);
	if (current.start !== expected) {
		errors.push(
			`${current.file}: weekly history is discontinuous after ${previous.file}; expected rangeStart ${expected}`,
		);
	}
}

if (errors.length > 0) {
	console.error(errors.map((error) => `- ${error}`).join("\n"));
	process.exit(1);
}

console.log("Changelog content is valid.");
