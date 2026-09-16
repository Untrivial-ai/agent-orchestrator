import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import type { TelemetryPolicyView } from "../../shared/telemetry-policy";
import { useTelemetryPolicyStore } from "../stores/telemetry-policy-store";
import { TelemetryConsentRenewalDialog } from "./TelemetryConsentRenewalDialog";

const { setEventsEnabled } = vi.hoisted(() => ({ setEventsEnabled: vi.fn() }));

vi.mock("../lib/bridge", () => ({
	aoBridge: { telemetry: { setEventsEnabled, getPolicy: vi.fn(), onPolicy: vi.fn(() => () => undefined) } },
}));

const renewal: TelemetryPolicyView = {
	eventsEnabled: false,
	consentGeneration: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca",
	updatedAt: "2026-08-28T10:15:30.000Z",
	acknowledged: true,
	consentRenewalRequired: true,
	state: "applied",
	environmentVeto: false,
	durabilitySupported: true,
};

beforeEach(() => {
	setEventsEnabled.mockReset();
	useTelemetryPolicyStore.setState({ view: renewal, loaded: true, saving: false, saveError: false });
});

it("stays hidden unless an earlier opt-in needs asking again", () => {
	useTelemetryPolicyStore.setState({ view: { ...renewal, consentRenewalRequired: false } });
	render(<TelemetryConsentRenewalDialog />);
	expect(screen.queryByTestId("telemetry-renewal-dialog")).toBeNull();
});

it("stays hidden when the environment would refuse the opt-in anyway", () => {
	useTelemetryPolicyStore.setState({ view: { ...renewal, environmentVeto: true, reason: "environment_veto" } });
	render(<TelemetryConsentRenewalDialog />);
	expect(screen.queryByTestId("telemetry-renewal-dialog")).toBeNull();
});

it("asks again and turns sharing on when the user agrees", async () => {
	setEventsEnabled.mockResolvedValue({ ...renewal, eventsEnabled: true, consentRenewalRequired: false, consentGeneration: "b3a27033-8afc-48a1-b6c7-070de12ccf98" });
	const user = userEvent.setup();
	render(<TelemetryConsentRenewalDialog />);

	expect(await screen.findByRole("dialog", { name: "Share error events?" })).toBeInTheDocument();
	await user.click(screen.getByRole("button", { name: "Turn on" }));

	expect(setEventsEnabled).toHaveBeenCalledWith(true);
	await waitFor(() => expect(screen.queryByTestId("telemetry-renewal-dialog")).toBeNull());
});

it("records an explicit no so it does not ask again", async () => {
	setEventsEnabled.mockResolvedValue({ ...renewal, consentRenewalRequired: false, consentGeneration: "b3a27033-8afc-48a1-b6c7-070de12ccf98" });
	const user = userEvent.setup();
	render(<TelemetryConsentRenewalDialog />);

	await user.click(await screen.findByRole("button", { name: "Keep off" }));

	expect(setEventsEnabled).toHaveBeenCalledWith(false);
	await waitFor(() => expect(screen.queryByTestId("telemetry-renewal-dialog")).toBeNull());
});

it("treats closing without choosing as not answered", async () => {
	const user = userEvent.setup();
	render(<TelemetryConsentRenewalDialog />);

	await screen.findByRole("dialog", { name: "Share error events?" });
	await user.keyboard("{Escape}");

	await waitFor(() => expect(screen.queryByTestId("telemetry-renewal-dialog")).toBeNull());
	expect(setEventsEnabled).not.toHaveBeenCalled();
	expect(useTelemetryPolicyStore.getState().view?.consentRenewalRequired).toBe(true);
});

it("keeps the question open and says so when saving the answer fails", async () => {
	setEventsEnabled.mockRejectedValue(new Error("write failed"));
	const user = userEvent.setup();
	render(<TelemetryConsentRenewalDialog />);

	await user.click(await screen.findByRole("button", { name: "Turn on" }));

	expect(await screen.findByRole("alert")).toBeInTheDocument();
	expect(screen.getByTestId("telemetry-renewal-dialog")).toBeInTheDocument();
});
