import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const card = readFileSync(new URL("./project-card.tsx", import.meta.url), "utf8");
const page = readFileSync(new URL("../app/project/[id].tsx", import.meta.url), "utf8");
const projects = readFileSync(new URL("../app/(tabs)/projects.tsx", import.meta.url), "utf8");

describe("project row", () => {
	// Matches a worker row: a divider, not a bordered card.
	it("is a flat row with a divider", () => {
		expect(card).toMatch(/row:\s*\{\s*borderBottomWidth:\s*rowDividerWidth/);
		expect(card).toMatch(/body:\s*\{\s*paddingHorizontal:\s*18/);
	});

	// The full-width button outweighed the project name; a footer pill does not,
	// and is still the only solid control on the row.
	it("carries the orchestrator as a footer strip with a compact pill", () => {
		expect(card).toContain("styles.footer");
		expect(card).toContain("<OrchestratorPill");
		expect(card).toContain("<OrchestratorIcon");
		expect(card).toContain("copy.short");
		expect(card).toMatch(/pill:\s*\{[^}]*height:\s*28/s);
	});

	it("keeps counts as quiet text rather than chips", () => {
		expect(card).toContain("projectCardSummary(row)");
		expect(card).not.toContain("<Chip");
		expect(card).not.toContain('<Feather name="folder"');
	});

	// Never nested: the pill sits over the row instead of inside its Pressable.
	it("opens the project from the row and the orchestrator from the pill", () => {
		expect(card).toContain("onPress={() => onOpenProject(row)}");
		expect(card).toContain('pointerEvents="box-none"');
		expect(projects).toContain('pathname: "/project/[id]"');
		expect(projects).toContain("onOrchestrator={openOrchestrator}");
	});
});

describe("project page", () => {
	it("puts the orchestrator on top of the project's workers", () => {
		expect(page).toContain("ListHeaderComponent={");
		expect(page).toContain("<ProjectPageHeader");
		expect(card).toContain("styles.stats");
	});

	// Reuses the board rather than copying it, so the two cannot drift.
	it("lists workers with the Workers board, archive included", () => {
		expect(page).toContain("<WorkerBoardList");
		expect(page).toContain("projectDetailSessions(");
	});
});
