import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const rendererDirectory = path.resolve(process.cwd(), "src/renderer");
const displayAttributes = new Set(["alt", "aria-label", "placeholder", "title"]);

// These are content/data, product names, technical units, or keyboard chords—not
// English UI copy. Keeping this allowlist exact makes newly introduced chrome fail.
const approvedLiterals: Record<string, readonly string[]> = {
	"components/BrowserPanel.tsx": [
		"AO Preview",
		"Demo app preview",
		"The worker exposed a local Vite app with",
		"ao preview",
		"Loaded",
		"$ npm run dev -- --host 127.0.0.1",
		"ready in 418 ms",
		"Local: http://localhost:5173/",
	],
	"components/CenterPane.tsx": ["px"],
	"components/CreateProjectFlow.tsx": ["my-workspace/", "web-app", "main"],
	"components/DaemonStartupLoader.tsx": ["Agent Orchestrator"],
	"components/ProjectSettingsForm.tsx": [
		"main", "ao",
		"No workflow settings for scratch projects.",
		"Tracker intake is not available for scratch projects.",
	],
	"components/SessionInspector.tsx": ["PR #"],
	"components/Sidebar.tsx": ["Agent Orchestrator", "daemon"],
	"components/WindowTitlebar.tsx": [
		"Alt+F4",
		"Ctrl+Z",
		"Ctrl+Y",
		"Ctrl+X",
		"Ctrl+C",
		"Ctrl+V",
		"Ctrl+A",
		"Ctrl+R",
		"Ctrl+Shift+I",
		"Ctrl+/",
	],
	"components/settings/ConnectMobileSetup.tsx": ["tailscale ip -4"],
	"components/settings/UpdatesSection.tsx": ["PR #"],
};

// The Chat surface predates this coverage gate and is intentionally being
// localized as a follow-up. Keep the deferral scoped to the new surface so
// hardcoded chrome elsewhere in the renderer still fails this test.
const deferredLocalizationFiles = new Set([
	"components/SessionInterfaceSwitch.tsx",
	"components/chat/ActivityRun.tsx",
	"components/chat/ChatComposer.tsx",
	"components/chat/ChatMarkdown.tsx",
	"components/chat/ChatStatusBanners.tsx",
	"components/chat/ChatTimelineItems.tsx",
	"components/chat/ChatWorkspace.tsx",
	"components/chat/ComposerSuggestMenu.tsx",
	"components/chat/ContextMeter.tsx",
	"components/chat/CopyButton.tsx",
	"components/chat/ElicitationCard.tsx",
	"components/chat/MermaidBlock.tsx",
	"components/chat/SessionChatSurface.tsx",
	"components/chat/TurnPlan.tsx",
	"components/chat/TurnSettingsBar.tsx",
	"components/chat/QueuedMessageDock.tsx",
]);

function rendererFiles(directory: string): string[] {
	return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
		const absolute = path.join(directory, entry.name);
		if (entry.isDirectory()) return rendererFiles(absolute);
		if (!entry.name.endsWith(".tsx") || entry.name.includes(".test.")) return [];
		return [absolute];
	});
}

function normalized(value: string): string {
	return value.replace(/\s+/g, " ").trim();
}

function literalBranches(expression: ts.Expression): string[] {
	if (ts.isStringLiteralLike(expression)) return [expression.text];
	if (ts.isTemplateExpression(expression)) {
		return [
			expression.head.text,
			...expression.templateSpans.flatMap((span) => [span.literal.text]),
		];
	}
	if (ts.isConditionalExpression(expression)) {
		return [...literalBranches(expression.whenTrue), ...literalBranches(expression.whenFalse)];
	}
	if (ts.isParenthesizedExpression(expression)) return literalBranches(expression.expression);
	if (ts.isBinaryExpression(expression) && expression.operatorToken.kind === ts.SyntaxKind.PlusToken) {
		return [...literalBranches(expression.left), ...literalBranches(expression.right)];
	}
	return [];
}

function potentialDisplayText(value: string): boolean {
	return /[A-Za-z]{2}/.test(value);
}

function approved(file: string, value: string): boolean {
	const relative = path.relative(rendererDirectory, file).replace(/\\/g, "/");
	if (deferredLocalizationFiles.has(relative)) return true;
	return approvedLiterals[relative]?.includes(value) ?? false;
}

describe("renderer localization coverage", () => {
	it("does not introduce hardcoded English JSX chrome", () => {
		const violations: string[] = [];
		for (const file of rendererFiles(rendererDirectory)) {
			const source = readFileSync(file, "utf8");
			const sourceFile = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
			const record = (node: ts.Node, rawValue: string) => {
				const value = normalized(rawValue);
				if (!value || !potentialDisplayText(value) || approved(file, value)) return;
				const line = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile)).line + 1;
				violations.push(`${path.relative(rendererDirectory, file)}:${line} ${JSON.stringify(value)}`);
			};
			const visit = (node: ts.Node) => {
				if (ts.isJsxText(node)) record(node, node.getText(sourceFile));
				if (ts.isJsxAttribute(node) && displayAttributes.has(node.name.getText(sourceFile)) && node.initializer) {
					if (ts.isStringLiteral(node.initializer)) record(node, node.initializer.text);
					if (ts.isJsxExpression(node.initializer) && node.initializer.expression) {
						for (const branch of literalBranches(node.initializer.expression)) record(node, branch);
					}
				}
				ts.forEachChild(node, visit);
			};
			visit(sourceFile);
		}

		expect(violations, violations.join("\n")).toEqual([]);
	});
});
