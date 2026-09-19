// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { CloudCpClient } from "../../lib/cloud-cp";
import { CloudFileTree } from "./CloudFileTree";

describe("CloudFileTree", () => {
	it("lazily expands directories and opens repository files", async () => {
		const getWorkspaceReviewTree = vi.fn(async (_org: string, _session: string, path?: string) => path === "src"
			? { path: "src", truncated: false, entries: [{ name: "App.tsx", path: "src/App.tsx", type: "file" as const, status: "modified" as const, size: 10 }] }
			: { path: "", truncated: false, entries: [{ name: "src", path: "src", type: "dir" as const, hasChanges: true }, { name: "README.md", path: "README.md", type: "file" as const, status: "unmodified" as const, size: 5 }] });
		const onOpenFile = vi.fn();
		const client = { getWorkspaceReviewTree } as unknown as CloudCpClient;
		render(
			<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
				<CloudFileTree baseUrl="https://cloud.test" client={client} orgId="org-1" sessionId="session-1" onOpenFile={onOpenFile} />
			</QueryClientProvider>,
		);
		await userEvent.click(await screen.findByRole("treeitem", { name: /src/ }));
		await userEvent.click(await screen.findByRole("treeitem", { name: /App\.tsx/ }));
		expect(getWorkspaceReviewTree).toHaveBeenCalledWith("org-1", "session-1", "src");
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx");
	});
});
