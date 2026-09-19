import { COMPANY } from "@ao/shared/constants";
import { getComparisonPages } from "@/lib/compare";
import {
	buildDeveloperResourcesSection,
	buildLlmsHeader,
	buildWhenToUseSection,
	stripMdxSyntax,
} from "@/lib/llms";
import { FAQ_ITEMS } from "../components/FAQSection/constants";


export const dynamic = "force-static";

export async function GET() {
	const comparisons = getComparisonPages();
	const baseUrl = COMPANY.MARKETING_URL;
	const docsUrl = COMPANY.DOCS_URL;

	const sections: string[] = [];

	// Header and guidance sections (same as llms.txt)
	sections.push(
		[
			...buildLlmsHeader(),
			"",
			...buildWhenToUseSection(),
			"",
			...buildDeveloperResourcesSection(),
		].join("\n"),
	);

	sections.push(
		[
			"---",
			"",
			"# Documentation",
			"",
			`- **[Documentation overview](${docsUrl}/index.html.md)**`,
			`- **[Tutorials](${docsUrl}/tutorials/index.html.md)**`,
			`- **[Run multiple Claude Code agents in parallel](${docsUrl}/tutorials/run-multiple-claude-code-agents-in-parallel/index.html.md)**`,
			`- **[What is an agent orchestrator](${docsUrl}/tutorials/what-is-an-agent-orchestrator/index.html.md)**`,
			`- **[Quickstart guide](${docsUrl}/quickstart/index.html.md)**`,
			`- **[Command-line interface (CLI) reference](${docsUrl}/cli/index.html.md)**`,
		].join("\n"),
	);

	// Comparison pages - full content
	if (comparisons.length > 0) {
		sections.push(
			[
				"---",
				"",
				"# Comparisons",
				"",
				...comparisons.flatMap((page) => [
					`## ${page.title}`,
					"",
					`URL: ${baseUrl}/compare/${page.slug}/`,
					"",
					stripMdxSyntax(page.content),
					"",
				]),
			].join("\n"),
		);
	}

	// FAQ section
	sections.push(
		[
			"---",
			"",
			"# FAQ",
			"",
			...FAQ_ITEMS.flatMap((item) => [
				`## ${item.question}`,
				"",
				item.answer,
				...(item.related
					? [
							"",
							`Related: [${item.related.label}](${baseUrl}${item.related.href})`,
						]
					: []),
				"",
			]),
		].join("\n"),
	);

	const content = sections.join("\n\n");

	return new Response(content, {
		headers: {
			"Content-Type": "text/plain; charset=utf-8",
			"Cache-Control": "public, max-age=3600, s-maxage=3600",
		},
	});
}
