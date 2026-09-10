import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AoBridge } from "../../../preload";
import { aoBridge } from "../../lib/bridge";
import { BrowserImportDialog } from "./BrowserImportDialog";

const source = {
	id: "a".repeat(32),
	name: "Google Chrome",
	family: "chromium" as const,
	profiles: [
		{ id: "b".repeat(32), name: "Default", default: true },
		{ id: "e".repeat(32), name: "Personal", default: false },
	],
	cookieSupport: "partial" as const,
	cookieSupportReason: "chromium-encryption-partial" as const,
	historySupport: true as const,
};

const firefoxSource = {
	id: "c".repeat(32),
	name: "Firefox",
	family: "firefox" as const,
	profiles: [{ id: "d".repeat(32), name: "default-release", default: true }],
	cookieSupport: "supported" as const,
	cookieSupportReason: "firefox-plaintext" as const,
	historySupport: true as const,
};

describe("BrowserImportDialog", () => {
	const originalBridge = aoBridge.browserProfiles;

	afterEach(() => {
		aoBridge.browserProfiles = originalBridge;
	});

	it("guides a detected profile into a new AO profile and reports completion", async () => {
		const importedProfile = {
			id: "11111111-1111-4111-8111-111111111111",
			name: "Google Chrome",
			createdAt: "2026-01-01T00:00:00.000Z",
			updatedAt: "2026-01-01T00:00:00.000Z",
		};
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => ({ profiles: [] })),
			create: vi.fn(),
			rename: vi.fn(),
			clear: vi.fn(),
			delete: vi.fn(),
			discoverImportSources: vi.fn(async () => ({ sources: [source, firefoxSource] })),
			import: vi.fn(async () => ({
				sourceName: source.name,
				entries: [{
					sourceProfileNames: ["Default"],
					destinationProfile: importedProfile,
					importedCookies: 12,
					skippedCookies: 1,
					importedHistoryEntries: 34,
					warnings: [{ code: "encrypted-cookies-skipped" as const, count: 1 }],
				}],
			})),
			onImportProgress: vi.fn(() => () => undefined),
		};
		aoBridge.browserProfiles = bridge;
		const onImported = vi.fn();

		render(<BrowserImportDialog onImported={onImported} onOpenChange={() => undefined} open />);
		expect(await screen.findByText("Google Chrome")).toBeInTheDocument();
		const sourcePicker = screen.getByRole("combobox", { name: "From" });
		expect(sourcePicker).toHaveTextContent("Google Chrome");
		await userEvent.click(sourcePicker);
		await userEvent.click(screen.getByRole("option", { name: /Firefox/ }));
		expect(sourcePicker).toHaveTextContent("Firefox");
		await userEvent.click(sourcePicker);
		await userEvent.click(screen.getByRole("option", { name: /Google Chrome/ }));
		expect(screen.getByRole("checkbox", { name: /Default/ })).toBeChecked();
		const personalProfile = screen.getByRole("checkbox", { name: /Personal/ });
		expect(personalProfile).not.toBeChecked();
		await userEvent.click(personalProfile);
		expect(screen.getByRole("radio", { name: "Keep profiles separate" })).toBeChecked();
		await userEvent.click(personalProfile);
		expect(screen.getByRole("textbox", { name: "Destination profile name" })).toHaveValue("Google Chrome");
		await userEvent.click(screen.getByRole("button", { name: "Start import" }));

		expect(await screen.findByText("Import complete")).toBeInTheDocument();
		expect(screen.getByText("12 cookies · 34 history entries")).toBeInTheDocument();
		await waitFor(() => expect(onImported).toHaveBeenCalledOnce());
		expect(bridge.import).toHaveBeenCalledWith(expect.objectContaining({
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: true,
			includeHistory: true,
			destination: { mode: "merge", name: "Google Chrome" },
		}));
	});

	it("clears a failed import when choosing another browser", async () => {
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => ({ profiles: [] })),
			create: vi.fn(),
			rename: vi.fn(),
			clear: vi.fn(),
			delete: vi.fn(),
			discoverImportSources: vi.fn(async () => ({ sources: [source, firefoxSource] })),
			import: vi.fn(async () => { throw new Error("Firefox cookie data is unavailable."); }),
			onImportProgress: vi.fn(() => () => undefined),
		};
		aoBridge.browserProfiles = bridge;
		const onImported = vi.fn();

		render(<BrowserImportDialog onImported={onImported} onOpenChange={() => undefined} open />);
		const sourcePicker = await screen.findByRole("combobox", { name: "From" });
		await userEvent.click(sourcePicker);
		await userEvent.click(screen.getByRole("option", { name: /Firefox/ }));
		await userEvent.click(screen.getByRole("button", { name: "Start import" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Firefox cookie data is unavailable.");
		expect(onImported).toHaveBeenCalledOnce();

		const retrySourcePicker = screen.getByRole("combobox", { name: "From" });
		await userEvent.click(retrySourcePicker);
		await userEvent.click(screen.getByRole("option", { name: /Google Chrome/ }));
		expect(retrySourcePicker).toHaveTextContent("Google Chrome");
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	it("explains how to recover when a source browser database cannot be opened", async () => {
		const braveSource = { ...source, name: "Brave" };
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => ({ profiles: [] })),
			create: vi.fn(),
			rename: vi.fn(),
			clear: vi.fn(),
			delete: vi.fn(),
			discoverImportSources: vi.fn(async () => ({ sources: [braveSource] })),
			import: vi.fn(async () => {
				throw new Error("Error invoking remote method 'browserProfiles:import:start': Error: unable to open database file");
			}),
			onImportProgress: vi.fn(() => () => undefined),
		};
		aoBridge.browserProfiles = bridge;

		render(<BrowserImportDialog onImported={() => undefined} onOpenChange={() => undefined} open />);
		await screen.findByText("Brave");
		await userEvent.click(screen.getByRole("button", { name: "Start import" }));

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("AO couldn't access Brave's profile database");
		expect(alert).toHaveTextContent("Fully close Brave, including background processes, then try again");
		expect(alert).toHaveTextContent("Encrypted cookies are handled separately");
		expect(alert).not.toHaveTextContent("Error invoking remote method");
	});

	it("keeps discovery failures visible and disables import", async () => {
		const bridge: AoBridge["browserProfiles"] = {
			list: vi.fn(async () => ({ profiles: [] })),
			create: vi.fn(),
			rename: vi.fn(),
			clear: vi.fn(),
			delete: vi.fn(),
			discoverImportSources: vi.fn(async () => { throw new Error("Browser discovery failed."); }),
			import: vi.fn(),
			onImportProgress: vi.fn(() => () => undefined),
		};
		aoBridge.browserProfiles = bridge;

		render(<BrowserImportDialog onImported={() => undefined} onOpenChange={() => undefined} open />);
		expect(await screen.findByRole("alert")).toHaveTextContent("Browser discovery failed.");
		expect(screen.getByRole("button", { name: "Start import" })).toBeDisabled();
	});
});
