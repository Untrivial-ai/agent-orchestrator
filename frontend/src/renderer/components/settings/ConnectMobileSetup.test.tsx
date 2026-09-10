import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { ConnectMobileSetup } from "./ConnectMobileSetup";

const noSecure = { enabled: false, reason: "" };

test("renders the LAN steps when the mode is lan", () => {
	render(
		<ConnectMobileSetup mode="lan" onModeChange={vi.fn()} enabled={true} secure={noSecure} onSecureChange={vi.fn()} />,
	);
	expect(screen.getByText(/same Wi-Fi as this computer/i)).toBeInTheDocument();
	expect(screen.queryByText(/tailscale ip -4/i)).not.toBeInTheDocument();
});

test("renders the Tailscale steps when the mode is tailscale", () => {
	render(
		<ConnectMobileSetup
			mode="tailscale"
			onModeChange={vi.fn()}
			enabled={true}
			secure={noSecure}
			onSecureChange={vi.fn()}
		/>,
	);
	expect(screen.getByText(/tailscale ip -4/i)).toBeInTheDocument();
	expect(screen.getByText(/scan the code below/i)).toBeInTheDocument();
});

test("reports the selected mode to its parent rather than keeping it locally", async () => {
	const onModeChange = vi.fn();
	render(
		<ConnectMobileSetup
			mode="lan"
			onModeChange={onModeChange}
			enabled={true}
			secure={noSecure}
			onSecureChange={vi.fn()}
		/>,
	);
	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));
	expect(onModeChange).toHaveBeenCalledWith("tailscale");
});

test("segments leave the tab order while the bridge is disabled", () => {
	render(
		<ConnectMobileSetup mode="lan" onModeChange={vi.fn()} enabled={false} secure={noSecure} onSecureChange={vi.fn()} />,
	);
	expect(screen.getByRole("radio", { name: "LAN" })).toHaveAttribute("tabindex", "-1");
});

test("reports the toggle state to its parent when secure pairing is switched on", async () => {
	const onSecureChange = vi.fn();
	render(
		<ConnectMobileSetup
			mode="tailscale"
			onModeChange={vi.fn()}
			enabled={true}
			secure={noSecure}
			onSecureChange={onSecureChange}
		/>,
	);
	await userEvent.click(screen.getByRole("switch", { name: /secure pairing/i }));
	expect(onSecureChange).toHaveBeenCalledWith(true);
});

test("shows the reason message only when secure pairing is on and the reason is known", () => {
	const { rerender } = render(
		<ConnectMobileSetup
			mode="tailscale"
			onModeChange={vi.fn()}
			enabled={true}
			secure={{ enabled: false, reason: "no_certs" }}
			onSecureChange={vi.fn()}
		/>,
	);
	expect(screen.queryByText(/HTTPS certificates/i)).not.toBeInTheDocument();

	rerender(
		<ConnectMobileSetup
			mode="tailscale"
			onModeChange={vi.fn()}
			enabled={true}
			secure={{ enabled: true, reason: "no_certs" }}
			onSecureChange={vi.fn()}
		/>,
	);
	expect(screen.getByText(/HTTPS certificates/i)).toBeInTheDocument();
});

test("renders nothing for an unknown reason from a newer daemon", () => {
	render(
		<ConnectMobileSetup
			mode="tailscale"
			onModeChange={vi.fn()}
			enabled={true}
			secure={{ enabled: true, reason: "some_future_reason" }}
			onSecureChange={vi.fn()}
		/>,
	);
	expect(screen.queryByText("mobile.securePairing.someFutureReason")).not.toBeInTheDocument();
});
