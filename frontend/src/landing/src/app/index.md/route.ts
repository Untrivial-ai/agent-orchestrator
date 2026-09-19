import { COMPANY } from "@ao/shared/constants";
import { FAQ_ITEMS } from "@/app/components/FAQSection/constants";
import {
  buildDeveloperResourcesSection,
  buildWhenToUseSection,
  MARKDOWN_HEADERS,
} from "@/lib/llms";

export const dynamic = "force-static";

export function GET() {
  const baseUrl = COMPANY.MARKETING_URL;
  const docsUrl = COMPANY.DOCS_URL;

  const lines: string[] = [
    `# ${COMPANY.NAME}, Mission control for coding agents`,
    "",
    `${COMPANY.NAME} is an open-source desktop application that lets developers orchestrate a fleet of coding agents in parallel, each in its own isolated Git worktree. It works with any CLI-based agent including Claude Code, Codex, OpenCode, Cursor, Aider, and 18 more. Agents work on different branches or features simultaneously without conflicts, and CI failures plus review feedback get routed back to the session that owns the branch. ${COMPANY.NAME} is free and open source under Apache 2.0.`,
    "",
    "## Features",
    "",
    "- **Fleet orchestration**: delegate to a main agent that plans, spawns workers, and keeps sessions moving, you stay the reviewer.",
    "- **Live board**: every session flows across one board, working, needs you, in review, ready to merge, with agent, branch, and PR state on each card.",
    "- **CI + review routing**: failed checks and review comments route back to the session that owns the branch, the right agent fixes the right thing.",
    "- **Any CLI agent**: Claude Code, Codex, OpenCode, Cursor, Aider, Goose, and 18 more. 23 harnesses total, with per-project agent choice.",
    "- **Isolated worktrees**: each agent runs in its own Git worktree. No merge conflicts, no stepping on each other's changes.",
    "- **Local first**: runs as a local daemon. Your code never leaves localhost.",
    "",
    "## Get started",
    "",
    `- [Download](${baseUrl}/download/)`,
    `- [Documentation](${docsUrl}/)`,
    `- [Tutorials](${docsUrl}/tutorials/)`,
    `- [GitHub](${COMPANY.GITHUB_URL})`,
    `- [Blog](${baseUrl}/blog/)`,
    `- [Changelog](${baseUrl}/changelog/)`,
    "",
    ...buildWhenToUseSection(),
    "",
    ...buildDeveloperResourcesSection(),
    "",
    "## FAQ",
    "",
    ...FAQ_ITEMS.flatMap((item) => [
      `### ${item.question}`,
      "",
      item.answer,
      ...(item.related
        ? ["", `Related: [${item.related.label}](${baseUrl}${item.related.href})`]
        : []),
      "",
    ]),
    "## Contact",
    "",
    `- Support: ${COMPANY.MAIL_TO.replace("mailto:", "")}`,
    `- Founders: ${COMPANY.FOUNDERS_EMAIL}`,
    `- [Discord](${COMPANY.DISCORD_URL})`,
    `- [X](${COMPANY.X_URL})`,
    "",
  ];

  return new Response(lines.join("\n"), { headers: MARKDOWN_HEADERS });
}
