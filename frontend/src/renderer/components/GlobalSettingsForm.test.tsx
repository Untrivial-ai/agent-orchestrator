import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { appI18n } from "../i18n";
import { GlobalSettingsForm, type GlobalSettingsSection } from "./GlobalSettingsForm";
import { useLocaleStore } from "../stores/locale-store";
import { useSoundNotificationsStore } from "../stores/sound-notifications-store";
import { useTerminalShellStore } from "../stores/terminal-shell-store";
import { useUiStore } from "../stores/ui-store";
import { useTelemetryPolicyStore } from "../stores/telemetry-policy-store";
import { TooltipProvider } from "./ui/tooltip";

const {
	getUpdate,
	setUpdate,
	getUiSettings,
	setUiSettings,
	updGetStatus,
	updCheck,
	updReturnHome,
	updDownload,
	updInstall,
	updOnStatus,
	getVersion,
	getDaemonStatus,
	navigateMock,
	writeText,
	openExternal,
	featListBuilds,
	featGetActive,
	getKeybindings,
	setKeybindings,
	setKeybindingRecording,
	setMacDifferentialUpdates,
	getTelemetryPolicy,
	setTelemetryEvents,
	onTelemetryPolicy,
} = vi.hoisted(() => ({
	getUpdate: vi.fn(),
	setUpdate: vi.fn(),
	getUiSettings: vi.fn(),
	setUiSettings: vi.fn(),
	updGetStatus: vi.fn(),
	updReturnHome: vi.fn(),
	updCheck: vi.fn(),
	updDownload: vi.fn(),
	updInstall: vi.fn(),
	updOnStatus: vi.fn(),
	getVersion: vi.fn(),
	getDaemonStatus: vi.fn(),
	navigateMock: vi.fn(),
	writeText: vi.fn(),
	openExternal: vi.fn(),
	featListBuilds: vi.fn(),
	featGetActive: vi.fn(),
	getKeybindings: vi.fn(),
	setKeybindings: vi.fn(),
	setKeybindingRecording: vi.fn(),
	setMacDifferentialUpdates: vi.fn().mockResolvedValue(undefined),
	// agent-switch visibility initializes at module load, before beforeEach can
	// install the per-test policy response. Preserve the bridge's Promise
	// contract for that initial read as well.
	getTelemetryPolicy: vi.fn().mockResolvedValue(undefined),
	setTelemetryEvents: vi.fn(),
	onTelemetryPolicy: vi.fn(),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@tanstack/react-router")>();
	return {
		...actual,
		useNavigate: () => navigateMock,
	};
});

vi.mock("../lib/platform", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/platform")>();
	return { ...actual, isWindowsPlatform: () => true };
});

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		app: { getVersion, openExternal },
		clipboard: { writeText },
		daemon: { getStatus: getDaemonStatus },
		updateSettings: {
			get: getUpdate,
			set: setUpdate,
			setMacDifferentialUpdates,
		},
		uiSettings: { get: getUiSettings, set: setUiSettings },
		keybindings: {
			get: getKeybindings,
			set: setKeybindings,
			setRecording: setKeybindingRecording,
		},
		updates: {
			getStatus: updGetStatus,
			check: updCheck,
			returnHome: updReturnHome,
			download: updDownload,
			install: updInstall,
			onStatus: updOnStatus,
		},
		featureBuilds: { list: featListBuilds, getActive: featGetActive },
		telemetry: { getPolicy: getTelemetryPolicy, setEventsEnabled: setTelemetryEvents, onPolicy: onTelemetryPolicy, getBootstrap: vi.fn(), capture: vi.fn() },
	},
}));

function renderForm(section: GlobalSettingsSection = "all") {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={qc}>
			<TooltipProvider>
				<GlobalSettingsForm section={section} />
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return qc;
}

beforeEach(async () => {
	for (const m of [
		getUpdate,
		setUpdate,
		getUiSettings,
		setUiSettings,
		updGetStatus,
		updCheck,
		updReturnHome,
		updDownload,
		updInstall,
		updOnStatus,
		navigateMock,
		writeText,
		openExternal,
		getVersion,
		getDaemonStatus,
		featListBuilds,
		featGetActive,
		getKeybindings,
		setKeybindings,
		setKeybindingRecording,
		setMacDifferentialUpdates,
		getTelemetryPolicy,
		setTelemetryEvents,
		onTelemetryPolicy,
	]) {
		m.mockReset();
	}
	getUpdate.mockResolvedValue({ enabled: true, channel: "latest", nightlyAck: false, feature: null });
	setUpdate.mockResolvedValue(undefined);
	getUiSettings.mockResolvedValue({ locale: "en", soundNotificationsEnabled: true, terminalShell: { kind: "auto" } });
	setUiSettings.mockImplementation(async (settings: { locale?: string; soundNotificationsEnabled?: boolean; terminalShell?: { kind: string; path?: string } }) => ({
		locale: "en",
		soundNotificationsEnabled: true,
		terminalShell: { kind: "auto" },
		...settings,
	}));
	updGetStatus.mockResolvedValue({ state: "idle" });
	updCheck.mockResolvedValue(undefined);
	updReturnHome.mockResolvedValue(undefined);
	updDownload.mockResolvedValue(undefined);
	updInstall.mockResolvedValue(undefined);
	updOnStatus.mockReturnValue(() => undefined);
	getVersion.mockResolvedValue("1.4.0");
	getDaemonStatus.mockResolvedValue({ state: "ready" });
	writeText.mockResolvedValue(undefined);
	openExternal.mockResolvedValue(undefined);
	featListBuilds.mockResolvedValue([]);
	featGetActive.mockResolvedValue(null);
	getKeybindings.mockResolvedValue({});
	setKeybindings.mockImplementation(async (overrides) => overrides);
	setKeybindingRecording.mockResolvedValue(undefined);
	setMacDifferentialUpdates.mockResolvedValue(undefined);
	getTelemetryPolicy.mockResolvedValue({ eventsEnabled: false, consentGeneration: "generation-off", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true });
	setTelemetryEvents.mockResolvedValue({ eventsEnabled: true, consentGeneration: "generation-on", updatedAt: "2026-08-28T10:15:31.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true });
	onTelemetryPolicy.mockReturnValue(() => undefined);
	// Locale defaults to English so existing copy assertions stay green.
	await appI18n.changeLanguage("en");
	useLocaleStore.setState({ locale: "en", loaded: false, saving: false, saveError: false });
	useSoundNotificationsStore.setState({ enabled: true, loaded: false, saving: false, saveError: false });
	useTerminalShellStore.setState({
		preference: { kind: "auto" },
		loaded: false,
		saving: false,
		saveError: false,
	});
	useUiStore.setState({ developerMode: false });
	useTelemetryPolicyStore.setState({ view: { eventsEnabled: false, consentGeneration: "generation-off", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true }, loaded: true, saving: false, saveError: false });
	document.documentElement.lang = "en";
});

describe("GlobalSettingsForm", () => {
	it("keeps Browser in its dedicated settings page", async () => {
		renderForm("general");
		expect(await screen.findByLabelText("Settings")).toBeInTheDocument();
		expect(document.querySelector('[data-section="browserProfiles"]')).not.toBeInTheDocument();
	});

	it("renders the settings sections", async () => {
		renderForm();
		expect(await screen.findByLabelText("Settings")).toBeInTheDocument();
		expect(screen.getByText("Appearance")).toBeInTheDocument();
		expect(screen.getByText("Language")).toBeInTheDocument();
		expect(await screen.findByText("Updates")).toBeInTheDocument();
		expect(screen.getByText("Advanced")).toBeInTheDocument();
		expect(screen.getByText("Report a problem")).toBeInTheDocument();
		// Report form is inline — no dialog, fields directly present.
		expect(screen.getByLabelText("Title")).toBeInTheDocument();
	});

	it("persists Developer Mode and reveals Feature Releases", async () => {
		const user = userEvent.setup();
		renderForm();
		const toggle = await screen.findByRole("switch", { name: "Developer Mode" });
		expect(toggle).toHaveAttribute("aria-checked", "false");

		await user.click(toggle);
		expect(window.localStorage.getItem("ao.developerMode")).toBe("true");
		expect(setMacDifferentialUpdates).toHaveBeenCalledWith(true);
		await user.click(screen.getByLabelText("Updates channel"));
		expect(await screen.findByRole("menuitem", { name: "Feature Releases" })).toBeInTheDocument();
	});

	it("shows the available feature builds after choosing Feature Releases", async () => {
		const user = userEvent.setup();
		featListBuilds.mockResolvedValue([]);
		useUiStore.getState().setDeveloperMode(true);
		renderForm();

		await user.click(await screen.findByLabelText("Updates channel"));
		await user.click(await screen.findByRole("menuitem", { name: "Feature Releases" }));
		expect(await screen.findByText("No live feature releases.")).toBeInTheDocument();
		expect(featListBuilds).toHaveBeenCalled();
	});

	it("switches settings labels to Simplified Chinese and persists locale", async () => {
		const user = userEvent.setup();
		renderForm();
		expect(await screen.findByLabelText("Settings")).toBeInTheDocument();
		expect(screen.getByLabelText("Language")).toBeInTheDocument();

		await user.click(screen.getByLabelText("Language"));
		await user.click(await screen.findByRole("menuitem", { name: "Simplified Chinese" }));

		await waitFor(() => expect(setUiSettings).toHaveBeenCalledWith({ locale: "zh-CN" }));
		await waitFor(() => expect(screen.getByText("语言")).toBeInTheDocument());
		expect(screen.getByText("主题")).toBeInTheDocument();
		expect(document.documentElement.lang).toBe("zh-CN");
		expect(useLocaleStore.getState().locale).toBe("zh-CN");
	});

	it("toggles sound notifications on and persists the change", async () => {
		const user = userEvent.setup();
		renderForm();
		const toggle = await screen.findByRole("switch", { name: "Sound notifications" });
		expect(toggle).toBeChecked();

		await user.click(toggle);

		await waitFor(() => expect(setUiSettings).toHaveBeenCalledWith({ soundNotificationsEnabled: false }));
		expect(toggle).not.toBeChecked();
	});

	it("shows pending daemon cleanup without claiming opt-out completed", async () => {
		setTelemetryEvents.mockResolvedValue({ eventsEnabled: false, consentGeneration: "generation-off-2", updatedAt: "2026-08-28T10:15:31.000Z", acknowledged: false, state: "cleanup_pending", environmentVeto: false, durabilitySupported: true, reason: "daemon_cleanup_pending" });
		useTelemetryPolicyStore.setState({ view: { eventsEnabled: true, consentGeneration: "generation-on", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true }, loaded: true });
		const user = userEvent.setup(); renderForm();
		await user.click(await screen.findByRole("switch", { name: "Share error events" }));
		expect(await screen.findByText("Telemetry is off locally. Daemon cleanup is still pending.")).toBeInTheDocument();
	});

	it("selects Git Bash as the default Windows terminal", async () => {
		const user = userEvent.setup();
		renderForm();
		const selector = await screen.findByLabelText("Default terminal");

		await user.click(selector);
		await user.click(await screen.findByRole("menuitem", { name: "Git Bash" }));

		await waitFor(() => expect(setUiSettings).toHaveBeenCalledWith({ terminalShell: { kind: "git-bash" } }));
	});

	it("discards an uncommitted custom shell path when editing is cancelled", async () => {
		const user = userEvent.setup();
		renderForm();

		await user.click(await screen.findByLabelText("Default terminal"));
		await user.click(await screen.findByRole("menuitem", { name: "Custom path" }));
		await waitFor(() => expect(setUiSettings).toHaveBeenCalledWith({ terminalShell: { kind: "custom" } }));

		setUiSettings.mockClear();
		await user.click(screen.getByRole("button", { name: "Edit Shell executable" }));
		const input = screen.getByLabelText("Shell executable");
		await user.type(input, "C:\\Tools\\bash.exe");
		await user.keyboard("{Escape}");

		expect(screen.queryByLabelText("Shell executable")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Edit Shell executable" })).toHaveTextContent("C:\\path\\to\\shell.exe");
		expect(setUiSettings).not.toHaveBeenCalled();
	});

	it("keeps the current sound notifications value and reports a persistence failure", async () => {
		setUiSettings.mockRejectedValue(new Error("disk full"));
		const user = userEvent.setup();
		renderForm();
		const toggle = await screen.findByRole("switch", { name: "Sound notifications" });

		await user.click(toggle);

		expect(await screen.findByRole("alert")).toHaveTextContent("Could not save the sound notifications preference.");
		expect(useSoundNotificationsStore.getState().enabled).toBe(true);
		expect(toggle).toBeChecked();
	});

	it("keeps the current language and reports a persistence failure", async () => {
		setUiSettings.mockRejectedValue(new Error("disk full"));
		const user = userEvent.setup();
		renderForm();
		await screen.findByLabelText("Settings");

		await user.click(screen.getByLabelText("Language"));
		await user.click(await screen.findByRole("menuitem", { name: "Simplified Chinese" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("Could not save the language preference.");
		expect(useLocaleStore.getState().locale).toBe("en");
		expect(screen.getByText("Appearance")).toBeInTheDocument();
	});

	it("closes settings with Escape", async () => {
		const user = userEvent.setup();
		renderForm();
		await screen.findByLabelText("Settings");

		await user.keyboard("{Escape}");

		// Escape is handled by the wrapping Radix Dialog, not the form itself
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("renders the report form inline without a dialog", async () => {
		renderForm();
		expect(await screen.findByLabelText("Title")).toBeInTheDocument();
		expect(screen.queryByRole("dialog", { name: "Report a problem" })).not.toBeInTheDocument();
	});

	it("shows the nightly warning when the nightly channel is loaded", async () => {
		getUpdate.mockResolvedValue({ enabled: true, channel: "nightly", nightlyAck: true, feature: null });
		renderForm();
		expect(await screen.findByText(/Nightly updates daily and may be unstable or cause data loss/i)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
	});

	it("auto-saves the updates channel while automatic updates are disabled", async () => {
		renderForm();
		await userEvent.click(await screen.findByRole("switch", { name: "Automatic Updates" }));
		await waitFor(() =>
			expect(setUpdate).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, channel: "latest" })),
		);
		await screen.findByLabelText("Updates channel");
		await userEvent.click(screen.getByLabelText("Updates channel"));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Nightly (Pre-release)" }));
		await waitFor(() =>
			expect(setUpdate).toHaveBeenCalledWith(
				expect.objectContaining({ channel: "nightly", enabled: false, nightlyAck: true, feature: null }),
			),
		);
		expect(screen.getByTestId("installed-update-channel")).toHaveTextContent("Stable");
		expect(await screen.findByText(/Nightly updates daily and may be unstable or cause data loss/i)).toBeInTheDocument();
	});

	it("checks the newly selected channel and explains how to switch after an update", async () => {
		let emit: (s: { state: string; version?: string; requestId?: string }) => void = () => undefined;
		updOnStatus.mockImplementation((cb: (s: unknown) => void) => {
			emit = cb as typeof emit;
			return () => undefined;
		});
		renderForm();
		await userEvent.click(await screen.findByLabelText("Updates channel"));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Nightly (Pre-release)" }));

		await waitFor(() =>
			expect(updCheck).toHaveBeenCalledWith(
				expect.objectContaining({
					settings: expect.objectContaining({ channel: "nightly" }),
					requestId: expect.stringMatching(/^channel-update-/),
				}),
			),
		);
		const requestId = updCheck.mock.calls.at(-1)?.[0]?.requestId;
		expect(requestId).toMatch(/^channel-update-/);
		act(() => emit({ state: "available", version: "1.5.0-nightly.202608271200", requestId }));
		expect(await screen.findByText("Update and restart to switch to Nightly."))
			.toBeInTheDocument();
	});

	it("auto-saves when automatic updates are toggled", async () => {
		renderForm();
		await userEvent.click(await screen.findByRole("switch", { name: "Automatic Updates" }));
		await waitFor(() =>
			expect(setUpdate).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, channel: "latest" })),
		);
		expect(screen.getByLabelText("Updates channel")).toBeInTheDocument();
	});

	it("hides the nightly warning on the stable channel", async () => {
		renderForm();
		await screen.findByText("Updates");
		expect(screen.queryByText(/Nightly updates daily and may be unstable or cause data loss/i)).not.toBeInTheDocument();
	});

	it("shows the current app version", async () => {
		renderForm();
		await waitFor(() => expect(screen.getByTestId("app-version")).toHaveTextContent("v1.4.0"));
		await waitFor(() => expect(screen.getByTestId("installed-update-channel")).toHaveTextContent("Stable"));
	});

	it("shows the installed Nightly channel separately from the selected update feed", async () => {
		getVersion.mockResolvedValue("1.4.0-nightly.202608271030");
		renderForm();
		// The badge labels which channel is installed, so it uses the short name;
		// "(Pre-release)" belongs in the picker where the choice is made.
		await waitFor(() => expect(screen.getByTestId("installed-update-channel")).toHaveTextContent("Nightly"));
	});

	it("shows an explicit idle update state and triggers a manual check", async () => {
		renderForm();
		await waitFor(() => expect(screen.getByTestId("app-version")).toHaveTextContent("v1.4.0"));
		expect(screen.getByText("Updates haven't been checked yet.")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Check for updates" }));
		expect(updCheck).toHaveBeenCalled();
	});

	it("shows immediate animated feedback while a manual check is pending", async () => {
		let finishCheck: () => void = () => undefined;
		updCheck.mockReturnValue(
			new Promise<void>((resolve) => {
				finishCheck = resolve;
			}),
		);
		renderForm();
		const button = await screen.findByRole("button", { name: "Check for updates" });

		await userEvent.click(button);

		expect(button).toBeDisabled();
		expect(button).toHaveTextContent("Checking for updates…");
		expect(button.querySelector("svg")).toHaveClass("animate-spin");
		expect(screen.getByTestId("update-status-line")).toHaveTextContent("Checking for updates…");

		act(() => finishCheck());
		await waitFor(() => expect(button).toBeEnabled(), { timeout: 1_500 });
	});

	it("stops manual loading when the updater completes before the check invocation settles", async () => {
		let emit: (status: { state: string; checkedAt?: number; requestId?: string }) => void = () => undefined;
		updOnStatus.mockImplementation((listener: (status: unknown) => void) => {
			emit = listener as typeof emit;
			return () => undefined;
		});
		updCheck.mockReturnValue(new Promise<void>(() => undefined));
		renderForm();
		const button = await screen.findByRole("button", { name: "Check for updates" });

		await userEvent.click(button);
		const requestId = updCheck.mock.calls[0]?.[0]?.requestId;
		expect(requestId).toMatch(/^manual-update-/);
		act(() => emit({ state: "not-available", checkedAt: Date.now() }));
		expect(screen.getByTestId("update-status-line")).toHaveTextContent("Checking for updates…");
		expect(button).toBeDisabled();

		act(() => emit({ state: "not-available", checkedAt: Date.now(), requestId }));

		await waitFor(() => expect(screen.getByTestId("update-status-line")).toHaveTextContent("You're on the latest version."), { timeout: 1_500 });
		expect(button).toBeEnabled();
	});

	it("stops manual loading when a completed check is immediately followed by the restored staged update", async () => {
		let emit: (status: { state: string; version?: string; requestId?: string }) => void = () => undefined;
		updOnStatus.mockImplementation((listener: (status: unknown) => void) => {
			emit = listener as typeof emit;
			return () => undefined;
		});
		updCheck.mockReturnValue(new Promise<void>(() => undefined));
		renderForm();
		const button = await screen.findByRole("button", { name: "Check for updates" });

		await userEvent.click(button);
		const requestId = updCheck.mock.calls[0]?.[0]?.requestId;
		expect(requestId).toMatch(/^manual-update-/);

		act(() => {
			emit({ state: "error", requestId });
			emit({ state: "downloaded", version: "1.2.3", requestId: "earlier-download" });
		});

		await waitFor(
			() => expect(screen.getByTestId("update-status-line")).toHaveTextContent("Downloaded. Restart to finish updating."),
			{ timeout: 1_500 },
		);
		expect(screen.getByRole("button", { name: "Restart & install" })).toBeInTheDocument();
		// The check control stays available alongside the restart action. Hiding it
		// once something was staged left a user whose staged build would not
		// install with a single dead button and no way to re-check.
		const recheck = screen.getByRole("button", { name: "Check for updates" });
		expect(recheck).toBeEnabled();
	});

	it("accepts a check click while a background check is already running", async () => {
		// Regression: gating the button on any "checking" status swallowed the
		// first click whenever Settings was opened during a background check --
		// as often as every 15 minutes on nightly -- which is what made the button
		// look like it needed a double-click. The main process serializes updater
		// operations, so a click during a background check simply queues.
		updGetStatus.mockResolvedValue({ state: "checking" });
		updCheck.mockResolvedValue(undefined);
		renderForm();

		const button = await screen.findByRole("button", { name: "Checking for updates…" });
		expect(button).toBeEnabled();
		await userEvent.click(button);

		expect(updCheck).toHaveBeenCalledTimes(1);
		expect(updCheck.mock.calls[0]?.[0]?.requestId).toMatch(/^manual-update-/);
	});

	it("shows when the updater last completed a check", async () => {
		const checkedAt = new Date("2026-08-19T12:51:00.000Z").getTime();
		updGetStatus.mockResolvedValue({ state: "not-available", checkedAt });
		renderForm();

		const formatted = new Intl.DateTimeFormat("en", { dateStyle: "medium", timeStyle: "short" }).format(checkedAt);
		expect(await screen.findByTestId("update-checked-at")).toHaveTextContent(`Last checked ${formatted}`);
		expect(screen.getByTestId("update-status-line")).toHaveTextContent("You're on the latest version.");
	});

	it("offers an Update button when an update is available and downloads it", async () => {
		let emit: (s: { state: string; version?: string; requestId?: string }) => void = () => undefined;
		updOnStatus.mockImplementation((cb: (s: unknown) => void) => {
			emit = cb as typeof emit;
			return () => undefined;
		});
		renderForm();
		await screen.findByRole("button", { name: "Check for updates" });
		act(() => emit({ state: "available", version: "1.2.3" }));
		const updateBtn = await screen.findByRole("button", { name: "Update to v1.2.3" });
		await userEvent.click(updateBtn);
		expect(updDownload).toHaveBeenCalled();
	});

	it("offers Restart & install once downloaded and asks before quitting", async () => {
		let emit: (s: { state: string; version?: string; requestId?: string }) => void = () => undefined;
		updOnStatus.mockImplementation((cb: (s: unknown) => void) => {
			emit = cb as typeof emit;
			return () => undefined;
		});
		renderForm();
		await screen.findByRole("button", { name: "Check for updates" });
		act(() => emit({ state: "downloaded", version: "1.2.3" }));
		const installBtn = await screen.findByRole("button", { name: /Restart & install/ });
		await userEvent.click(installBtn);

		// Installing quits the app, which costs a turn on any chat session running
		// a daemon-owned driver, so the click opens the confirmation instead of
		// tearing the app down on one click.
		expect(updInstall).not.toHaveBeenCalled();
		expect(useUiStore.getState().updateInstallPromptOpen).toBe(true);
	});

	it("shows a non-error restart nudge when automatic checks keep failing on the network", async () => {
		updGetStatus.mockResolvedValue({ state: "not-available", staleCheckNudge: true });
		renderForm();
		const nudge = await screen.findByText(
			"Updates haven't been able to check for a while — restarting the app usually fixes this.",
		);
		expect(nudge).toBeInTheDocument();
		// The nudge is a warning, not an error, and the normal status still shows.
		expect(screen.getByText("You're on the latest version.")).toBeInTheDocument();
	});

	it("shows localized restart guidance for a net:: error status", async () => {
		updGetStatus.mockResolvedValue({ state: "error", message: "net::ERR_FAILED", netError: true });
		renderForm();
		const guidance = await screen.findByText(
			"Couldn't reach the update server — the app's network connection appears stuck. Restarting the app usually fixes this.",
		);
		expect(guidance).toBeInTheDocument();
	});

	it("opens feedback from settings and copies redacted report drafts", async () => {
		const user = userEvent.setup();
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		getVersion.mockResolvedValue("9.9.9-test");
		getDaemonStatus.mockResolvedValue({
			state: "ready",
			message: "Listening at http://127.0.0.1:31001?token=secret",
		});
		renderForm();

		await user.type(await screen.findByLabelText("Title"), "Create project fails in /Users/alice/private-repo");
		await user.type(
			screen.getByLabelText("What happened?"),
			"Open http://127.0.0.1:5173/projects/demo?access_token=local-secret and click Create. Show a clear prerequisite error.",
		);
		expect(screen.queryByRole("combobox", { name: "Report type" })).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Include safe diagnostics")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Expected behavior")).not.toBeInTheDocument();
		expect(screen.getByRole("group", { name: "Report destination" })).toBeInTheDocument();
		expect(screen.queryByRole("radiogroup", { name: "Report destination" })).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Report preview")).not.toBeInTheDocument();

		expect(screen.getByRole("button", { name: /copy & create github issue/i })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /copy & open discord/i })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /copy & open email/i })).toBeInTheDocument();
		expect(screen.getByLabelText("What happened?")).toHaveClass("resize-none");
		await user.click(screen.getByRole("button", { name: /copy & create github issue/i }));

		await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
		const copied = writeText.mock.calls[0][0] as string;
		expect(copied).toContain("Create project fails");
		expect(copied).toContain("AO version: 9.9.9-test");
		expect(copied).toContain("Daemon: ready");
		expect(copied).toContain("[redacted-local-path]");
		expect(copied).toContain("[redacted-local-url]");
		expect(copied).not.toContain("/Users/alice");
		expect(copied).not.toContain("local-secret");
		expect(copied).not.toContain("## Type");
		expect(copied).not.toContain("Generated locally by AO");
		expect(openExternal).toHaveBeenCalledWith(
			expect.stringContaining("https://github.com/Untrivial-ai/agent-orchestrator/issues/new"),
		);
		expect(open).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Title")).toHaveValue("");
		expect(screen.getByLabelText("What happened?")).toHaveValue("");
	});

	it("opens Discord with an official invite and email with the support mailbox", async () => {
		const user = userEvent.setup();
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		getVersion.mockRejectedValue(new Error("version unavailable"));
		getDaemonStatus.mockRejectedValue(new Error("daemon unavailable"));
		renderForm();

		await user.type(await screen.findByLabelText("Title"), "Need help with setup");
		await user.type(screen.getByLabelText("What happened?"), "The setup flow stalls after the first prompt.");

		expect(screen.getByRole("button", { name: /copy & open discord/i })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /copy & open email/i })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /copy & open discord/i }));
		await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
		expect(writeText.mock.calls[0][0]).toContain("**AO feedback**");
		expect(screen.getByText("Discord draft copied.")).toBeInTheDocument();
		expect(screen.getByLabelText("Title")).toHaveValue("");
		expect(screen.getByLabelText("What happened?")).toHaveValue("");

		expect(screen.getByRole("button", { name: /copy & open email/i })).toBeDisabled();
		await user.type(screen.getByLabelText("Title"), "Need help with setup");
		expect(screen.queryByText("Discord draft copied.")).not.toBeInTheDocument();
		await user.type(screen.getByLabelText("What happened?"), "The setup flow stalls after the first prompt.");
		await user.click(screen.getByRole("button", { name: /copy & open email/i }));

		await waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
		expect(writeText.mock.calls[0][0]).toContain("Daemon: unknown");
		expect(writeText.mock.calls[1][0]).toContain("To: prateek@untrivial.ai");
		expect(writeText.mock.calls[1][0]).toContain("AO feedback");
		expect(openExternal).toHaveBeenCalledWith("https://discord.com/invite/UZv7JjxbwG");
		expect(openExternal).toHaveBeenCalledWith(expect.stringContaining("mailto:prateek@untrivial.ai"));
		expect(open).not.toHaveBeenCalled();
	});

	it("keeps the report form to title and details while tailoring placeholder guidance", async () => {
		renderForm();

		expect(await screen.findByLabelText("Title")).toHaveAttribute("placeholder", "Brief Title");
		expect(screen.getByLabelText("What happened?")).toHaveAttribute(
			"placeholder",
			"Share what happened, what you expected, and how to reproduce it.",
		);
		expect(screen.queryByLabelText("Expected behavior")).not.toBeInTheDocument();
		expect(screen.queryByRole("combobox", { name: "Report type" })).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Include safe diagnostics")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Report preview")).not.toBeInTheDocument();
	});

	it("surfaces a Return action for a persisted feature pin", async () => {
		// A pin persists in settings but is not yet running; updates are on the stable channel.
		getUpdate.mockResolvedValue({ enabled: true, channel: "latest", nightlyAck: false, feature: { pr: 2270 } });
		featGetActive.mockResolvedValue(null);
		renderForm();
		// The concealed pin is announced even though the channel option/picker are hidden.
		expect(await screen.findByText("PR #2270 is pinned but not yet installed.")).toBeInTheDocument();
		// The fall-home copy must be truthful: automatic updates keep tracking the pin,
		// they do NOT silently return the user home on the next check.
		expect(
			screen.getByText(
				/Automatic updates, if enabled, keep tracking PR #2270 until you return home or the build retires\./i,
			),
		).toBeInTheDocument();
		await userEvent.click(screen.getByLabelText("Updates channel"));
		await userEvent.keyboard("{Escape}");
		// Return delegates to the single updater-serialized returnHome operation.
		await userEvent.click(screen.getByRole("button", { name: "Return to Stable" }));
		await waitFor(() => expect(updReturnHome).toHaveBeenCalledWith(expect.any(String)));
		expect(updCheck).not.toHaveBeenCalled();
	});

	it("returns to Stable, then auto-progresses check -> download -> install", async () => {
		getUpdate.mockResolvedValue({ enabled: true, channel: "latest", nightlyAck: false, feature: { pr: 2270 } });
		featGetActive.mockResolvedValue({ pr: 2270 });
		let emit: (s: { state: string; version?: string; requestId?: string }) => void = () => undefined;
		updOnStatus.mockImplementation((cb: (s: unknown) => void) => {
			emit = cb as typeof emit;
			return () => undefined;
		});
		renderForm();

		const returnBtn = await screen.findByRole("button", { name: "Return to Stable" });
		await userEvent.click(returnBtn);

		await waitFor(() => expect(updReturnHome).toHaveBeenCalledWith(expect.any(String)));
		const requestId = updReturnHome.mock.calls[0]?.[0] as string;

		act(() => emit({ state: "available", version: "1.3.0", requestId }));
		await waitFor(() => expect(updDownload).toHaveBeenCalledWith(requestId));
		act(() => emit({ state: "downloaded", version: "1.3.0", requestId }));
		await waitFor(() => expect(updInstall).toHaveBeenCalled());
	});
});
