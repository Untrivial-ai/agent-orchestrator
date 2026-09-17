import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { WorkspaceEntryIcon } from "./WorkspaceEntryIcon";

describe("WorkspaceEntryIcon", () => {
	it("resolves distinct file icons and named folder icons", () => {
		render(
			<>
				<WorkspaceEntryIcon kind="file" name="App.tsx" testId="tsx" />
				<WorkspaceEntryIcon kind="file" name="README.md" testId="markdown" />
				<WorkspaceEntryIcon kind="dir" name="src" testId="src" />
			</>,
		);
		expect(screen.getByTestId("tsx").innerHTML).not.toBe(screen.getByTestId("markdown").innerHTML);
		expect(screen.getByTestId("src").tagName).toBe("svg");
	});
});
