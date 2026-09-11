import { COMPANY } from "@ao/shared/constants";
import { FAQ_ITEMS } from "@/app/components/FAQSection/constants";
import { getComparisonPages } from "./compare";
import { getDocPage, getDocsNav, type DocsNavItem } from "./docs";

export function stripMdxSyntax(content: string): string {
	return (
		content
			// Remove import statements
			.replace(/^import\s+.*$/gm, "")
			// Remove JSX component tags (e.g. <Video ... />, <Component>...</Component>)
			.replace(/<[A-Z]\w*\b[^>]*\/>/g, "")
			.replace(/<[A-Z]\w*\b[^>]*>[\s\S]*?<\/[A-Z]\w*>/g, "")
			// Clean up excessive blank lines
			.replace(/\n{3,}/g, "\n\n")
			.trim()
	);
}

export function buildLlmsHeader(): string[] {
	return [
		`# ${COMPANY.NAME}`,
		"",
		"> Open-source desktop application and local CLI (`ao`) to run 10+ parallel AI coding agents in isolated Git worktrees without file conflicts or API proxying.",
		"",
		`${COMPANY.NAME} is an open-source desktop application that lets developers run multiple AI coding agents in parallel, each in its own isolated Git worktree. It works with any CLI-based agent including Claude Code, OpenCode, and OpenAI Codex. Agents can work on different branches or features simultaneously without conflicts. ${COMPANY.NAME} is free, does not proxy API calls, and supports macOS, Windows, and Linux.`,
	];
}

export function buildWhenToUseSection(
	{ referenceDocumentationSection = false }: {
		referenceDocumentationSection?: boolean;
	} = {},
): string[] {
	const documentationDirection = referenceDocumentationSection
		? "To learn the product, use the Documentation section below."
		: "To learn the product, start with the docs index at https://useao.dev/docs/.";

	return [
		"## When to use Agent Orchestrator",
		"",
		"Reach for Agent Orchestrator when you need to:",
		"",
		"- Run several coding agents (Claude Code, Codex, OpenCode, or any CLI agent) at the same time on one repository without them stepping on each other, each agent gets an isolated Git worktree and its own branch.",
		"- Orchestrate agent work through the desktop app and local `ao` CLI: create workspaces, launch agents with a prompt, open terminals, and track tasks.",
		"- Schedule recurring agent runs (automations) that execute a prompt on a cron-like schedule in a fresh or existing workspace.",
		"- Automatically route CI failures and review feedback to the agent session that owns the branch, so the right agent can handle failures without manual babysitting.",
		"- Review diffs, manage ports, and monitor many concurrent agent sessions from one dashboard.",
		"",
		`Agent Orchestrator is not a coding agent itself; it is the local workspace and orchestration layer the agents run in. If you are an AI agent inside an AO-managed session, use the installed \`ao\` CLI. ${documentationDirection}`,
	];
}

export function buildDeveloperResourcesSection(
	{ includeDocumentationLinks = true }: {
		includeDocumentationLinks?: boolean;
	} = {},
): string[] {
	const baseUrl = COMPANY.MARKETING_URL;
	const docsUrl = COMPANY.DOCS_URL;
	return [
		"## Developer resources",
		"",
		...(includeDocumentationLinks
			? [
					`- [Documentation](${docsUrl}/): product and workflow documentation`,
					`- [Quickstart](${docsUrl}/quickstart/): install and first-run guide`,
					`- [CLI](${docsUrl}/cli/): local \`ao\` command reference`,
				]
			: []),
		`- [Agent instructions](${baseUrl}/agents.md): when and how AI agents should use Agent Orchestrator`,
		`- [Blog llms.txt](${baseUrl}/blog/llms.txt): scoped index of blog posts`,
		`- [GitHub](${COMPANY.GITHUB_URL}): source code and releases`,
	];
}

function docSlugFromUrl(url: string): string[] {
	const path = url.replace(/^\/docs\/?/, "");
	return path ? path.split("/") : [];
}

function renderDocumentationItem(item: DocsNavItem, depth: number): string[] {
	const indent = "  ".repeat(depth);
	if (item.separator) return [`${indent}- **${item.title}**`];

	const page = item.url ? getDocPage(docSlugFromUrl(item.url)) : undefined;
	const href = item.url
		? `${COMPANY.MARKETING_URL}${item.url.replace(/\/+$/, "")}/index.html.md`
		: undefined;
	const link = href ? `[${item.title}](${href})` : item.title;
	const label = item.items && item.items.length > 0 ? `**${link}**` : link;
	const description = page?.description ? `: ${page.description}` : "";
	const lines = [`${indent}- ${label}${description}`];

	for (const child of item.items ?? []) {
		lines.push(...renderDocumentationItem(child, depth + 1));
	}

	return lines;
}

export function buildDocumentationSection(): string[] {
	const overview = getDocPage([]);
	const overviewDescription = overview?.description
		? `: ${overview.description}`
		: "";
	const lines = [
		"## Documentation",
		"",
		`- **[Documentation overview](${COMPANY.DOCS_URL}/index.html.md)**${overviewDescription}`,
	];

	for (const item of getDocsNav()) {
		if (item.separator) {
			if (lines[lines.length - 1] !== "") lines.push("");
			lines.push(`### ${item.title}`, "");
			continue;
		}
		lines.push(...renderDocumentationItem(item, 0));
	}

	return lines;
}

export function buildLlmsTxt(): string {
	const comparisons = getComparisonPages();
	const baseUrl = COMPANY.MARKETING_URL;

	const lines: string[] = [
		...buildLlmsHeader(),
		"",
		...buildWhenToUseSection({ referenceDocumentationSection: true }),
		"",
		...buildDeveloperResourcesSection({ includeDocumentationLinks: false }),
		`- [Full LLM context](${baseUrl}/llms-full.txt): combined AO overview, developer resources, comparisons, and FAQ`,
		"",
		...buildDocumentationSection(),
		"",
		"## Comparisons",
		"",
		...comparisons.map(
			(page) => `- [${page.title}](${baseUrl}/compare/${page.slug}/)`,
		),
		"",
		"## FAQ",
		"",
		...FAQ_ITEMS.flatMap((item) => [
			`### ${item.question}`,
			"",
			item.answer,
			"",
		]),
	];

	return lines.join("\n");
}

export const MARKDOWN_HEADERS = {
	"Content-Type": "text/markdown; charset=utf-8",
	"Cache-Control": "public, max-age=3600, s-maxage=3600",
	Vary: "Accept",
} as const;
