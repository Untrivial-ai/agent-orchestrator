import { beforeEach, describe, expect, it, vi } from "vitest";

const getUiSettings = vi.fn();
const setUiSettings = vi.fn();

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		uiSettings: {
			get: (...args: unknown[]) => getUiSettings(...args),
			set: (...args: unknown[]) => setUiSettings(...args),
		},
	},
}));

import { terminalShellRequestValue, useTerminalShellStore } from "./terminal-shell-store";

describe("terminal-shell-store", () => {
	beforeEach(() => {
		getUiSettings.mockReset();
		setUiSettings.mockReset();
		getUiSettings.mockResolvedValue({
			locale: "en",
			soundNotificationsEnabled: true,
			terminalShell: { kind: "auto" },
		});
		setUiSettings.mockImplementation(async (settings) => settings);
		useTerminalShellStore.setState({
			preference: { kind: "auto" },
			loaded: false,
			saving: false,
			saveError: false,
		});
	});

	it("loads the persisted terminal shell", async () => {
		getUiSettings.mockResolvedValue({
			locale: "en",
			soundNotificationsEnabled: true,
			terminalShell: { kind: "git-bash" },
		});

		await useTerminalShellStore.getState().load();

		expect(useTerminalShellStore.getState()).toMatchObject({ preference: { kind: "git-bash" }, loaded: true });
	});

	it("persists a custom executable path", async () => {
		await useTerminalShellStore.getState().setPreference({ kind: "custom", path: "C:\\Tools\\bash.exe" });

		expect(setUiSettings).toHaveBeenCalledWith({
			terminalShell: { kind: "custom", path: "C:\\Tools\\bash.exe" },
		});
		expect(terminalShellRequestValue(useTerminalShellStore.getState().preference)).toBe("C:\\Tools\\bash.exe");
	});

	it("uses auto while a custom path is blank", () => {
		expect(terminalShellRequestValue({ kind: "custom" })).toBe("auto");
	});

	it("keeps the current preference when persistence fails", async () => {
		setUiSettings.mockRejectedValue(new Error("disk full"));

		await useTerminalShellStore.getState().setPreference({ kind: "git-bash" });

		expect(useTerminalShellStore.getState()).toMatchObject({
			preference: { kind: "auto" },
			saving: false,
			saveError: true,
		});
	});
});
