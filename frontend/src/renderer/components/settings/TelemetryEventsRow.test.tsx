import { render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { useTelemetryPolicyStore } from "../../stores/telemetry-policy-store";
import { TelemetryEventsRow } from "./GeneralSettingsSection";

const { setEventsEnabled } = vi.hoisted(() => ({ setEventsEnabled: vi.fn() }));
vi.mock("../../lib/bridge", () => ({
	aoBridge: { telemetry: { setEventsEnabled, getPolicy: vi.fn(), onPolicy: vi.fn(() => () => undefined) } },
}));

beforeEach(() => {
	setEventsEnabled.mockReset();
	useTelemetryPolicyStore.setState({
		view: {
			eventsEnabled: false, consentGeneration: "old-generation", updatedAt: "",
			acknowledged: true, consentRenewalRequired: true, state: "applied",
			environmentVeto: false, durabilitySupported: true, reason: "release_blocked",
		},
		saving: false, saveError: false,
	});
});

it("keeps identity disclosure visible even when diagnostics are release-blocked", async () => {
	const user = userEvent.setup();
	render(<TelemetryEventsRow />);
	expect(screen.getByText(/authenticated GitHub handle linked to usage and agent spawns/)).toBeVisible();
	expect(screen.getByText(/Error reporting is disabled/)).toBeVisible();
	await user.click(screen.getByRole("switch", { name: "Share diagnostics and GitHub identity" }));
	expect(setEventsEnabled).toHaveBeenCalledWith(true);
});
