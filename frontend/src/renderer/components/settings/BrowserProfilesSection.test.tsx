import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AoBridge } from "../../../preload";
import { BrowserProfilesSection } from "./BrowserProfilesSection";

const profile = {
	id: "11111111-1111-4111-8111-111111111111",
	name: "Work",
	createdAt: "2026-01-01T00:00:00.000Z",
	updatedAt: "2026-01-01T00:00:00.000Z",
};

describe("BrowserProfilesSection", () => {
	let originalBridge: AoBridge["browserProfiles"];

	afterEach(() => {
		if (window.ao) window.ao.browserProfiles = originalBridge;
	});

	it("loads profiles and wires create, rename, clear, and delete actions", async () => {
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => ({ profiles: [profile] })),
			create: vi.fn(async (name: string) => ({ ...profile, id: "22222222-2222-4222-8222-222222222222", name })),
			rename: vi.fn(async (input: { id: string; name: string }) => ({ ...profile, ...input })),
			clear: vi.fn(async () => undefined),
			delete: vi.fn(async () => undefined),
			discoverImportSources: vi.fn(async () => ({ sources: [] })),
			import: vi.fn(async () => ({ sourceName: "", entries: [] })),
			onImportProgress: vi.fn(() => () => undefined),
		};
		originalBridge = window.ao!.browserProfiles;
		window.ao!.browserProfiles = bridge;

		render(<BrowserProfilesSection />);
		expect(await screen.findByText("Work")).toBeInTheDocument();
		expect(bridge.list).toHaveBeenCalledOnce();

		const createInput = screen.getByRole("textbox", { name: "Profile name" });
		await userEvent.type(createInput, "Personal");
		await userEvent.click(screen.getByRole("button", { name: "Create profile" }));
		await waitFor(() => expect(bridge.create).toHaveBeenCalledWith("Personal"));
		expect(await screen.findByText("Personal")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Actions for Work" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Rename" }));
		const renameInput = await screen.findByRole("textbox", { name: "New name for Work" });
		await waitFor(() => expect(renameInput).toHaveFocus());
		await userEvent.clear(renameInput);
		await userEvent.type(renameInput, "Office{Enter}");
		await waitFor(() => expect(bridge.rename).toHaveBeenCalledWith({ id: profile.id, name: "Office" }));
		expect(await screen.findByText("Office")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Actions for Office" })).toHaveFocus();

		await userEvent.click(screen.getByRole("button", { name: "Actions for Office" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Clear data" }));
		expect(bridge.clear).not.toHaveBeenCalled();
		await userEvent.click(await screen.findByRole("button", { name: "Clear data" }));
		await waitFor(() => expect(bridge.clear).toHaveBeenCalledWith(profile.id));
		await userEvent.click(screen.getByRole("button", { name: "Actions for Office" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Delete profile" }));
		expect(bridge.delete).not.toHaveBeenCalled();
		await userEvent.click(await screen.findByRole("button", { name: "Delete profile" }));
		await waitFor(() => expect(bridge.delete).toHaveBeenCalledWith(profile.id));
	});

	it("cancels an inline rename with Escape and keeps the saved name", async () => {
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => ({ profiles: [profile] })),
			create: vi.fn(),
			rename: vi.fn(),
			clear: vi.fn(),
			delete: vi.fn(),
			discoverImportSources: vi.fn(async () => ({ sources: [] })),
			import: vi.fn(async () => ({ sourceName: "", entries: [] })),
			onImportProgress: vi.fn(() => () => undefined),
		};
		originalBridge = window.ao!.browserProfiles;
		window.ao!.browserProfiles = bridge;

		render(<BrowserProfilesSection />);
		await userEvent.click(await screen.findByRole("button", { name: "Actions for Work" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Rename" }));
		const renameInput = await screen.findByRole("textbox", { name: "New name for Work" });
		await waitFor(() => expect(renameInput).toHaveFocus());
		await userEvent.type(renameInput, " draft{Escape}");

		expect(screen.queryByRole("textbox", { name: "New name for Work" })).not.toBeInTheDocument();
		expect(screen.getByText("Work")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Actions for Work" })).toHaveFocus();
		expect(bridge.rename).not.toHaveBeenCalled();
	});

	it("surfaces a recoverable load error", async () => {
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => {
				throw new Error("Registry is corrupt");
			}),
			create: vi.fn(),
			rename: vi.fn(),
			clear: vi.fn(),
			delete: vi.fn(),
			discoverImportSources: vi.fn(async () => ({ sources: [] })),
			import: vi.fn(async () => ({ sourceName: "", entries: [] })),
			onImportProgress: vi.fn(() => () => undefined),
		};
		originalBridge = window.ao!.browserProfiles;
		window.ao!.browserProfiles = bridge;

		render(<BrowserProfilesSection />);
		expect(await screen.findByRole("alert")).toHaveTextContent("Registry is corrupt");
	});
});
