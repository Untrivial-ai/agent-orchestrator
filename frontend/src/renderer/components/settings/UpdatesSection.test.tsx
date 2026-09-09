import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { UpdatesSection } from "./UpdatesSection";
import { useUiStore } from "../../stores/ui-store";
import type { UpdateStatus } from "../../../main/update-settings";

const {
	updGetStatus,
	updOnStatus,
	settingsGet,
	settingsSet,
	getVersion,
	featureBuildsList,
	featureBuildsGetActive,
	telemetryCapture,
} = vi.hoisted(() => ({
	updGetStatus: vi.fn(),
	updOnStatus: vi.fn(),
	settingsGet: vi.fn(),
	settingsSet: vi.fn(),
	getVersion: vi.fn(),
	featureBuildsList: vi.fn(),
	featureBuildsGetActive: vi.fn(),
	telemetryCapture: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		app: { getVersion },
		updates: {
			getStatus: updGetStatus,
			onStatus: updOnStatus,
			install: vi.fn(),
			download: vi.fn(),
			check: vi.fn(),
			returnHome: vi.fn(),
		},
		updateSettings: { get: settingsGet, set: settingsSet },
		featureBuilds: { getActive: featureBuildsGetActive, list: featureBuildsList },
	},
}));

vi.mock("../../lib/telemetry", () => ({
	captureRendererEvent: telemetryCapture,
	releaseChannelFrom: vi.fn(),
	setReleaseChannelContext: vi.fn(),
}));

vi.mock("../../hooks/useRequestUpdateInstall", () => ({
	useRequestUpdateInstall: () => () => undefined,
}));

function renderUpdates() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<UpdatesSection />
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	for (const m of [updGetStatus, updOnStatus, settingsGet, settingsSet, getVersion, featureBuildsList, featureBuildsGetActive, telemetryCapture]) {
		m.mockReset();
	}
	updOnStatus.mockReturnValue(() => undefined);
	settingsGet.mockResolvedValue({ enabled: false, channel: "latest", nightlyAck: true, feature: null });
	settingsSet.mockResolvedValue({ enabled: false, channel: "latest", nightlyAck: true, feature: null });
	getVersion.mockResolvedValue("0.12.11-nightly.202609070300");
	featureBuildsList.mockResolvedValue([]);
	featureBuildsGetActive.mockResolvedValue(null);
	updGetStatus.mockResolvedValue({ state: "idle" } satisfies UpdateStatus);
	useUiStore.setState({ developerMode: false });
});

it("renders the installed nightly build time as the device-local instant", async () => {
	// The stamp 202609070300 encodes 03:00 UTC. The expected string is derived
	// from that absolute instant, so the assertion holds in every timezone —
	// and it can only hold at all when the stamp is parsed as UTC, never as
	// local wall time (issue #5059).
	renderUpdates();

	const builtAt = new Intl.DateTimeFormat("en", { dateStyle: "medium", timeStyle: "short" }).format(
		new Date(Date.UTC(2026, 8, 7, 3, 0)),
	);
	expect(await screen.findByText(`Built ${builtAt}`)).toBeVisible();
});
