import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { ANDROID_PLAY_STORE_URL, ConnectMobileGetApp, IOS_APP_STORE_URL } from "./ConnectMobileGetApp";

const { openExternal } = vi.hoisted(() => ({ openExternal: vi.fn() }));

vi.mock("../../lib/bridge", () => ({
	aoBridge: { app: { openExternal } },
}));

beforeEach(() => {
	openExternal.mockReset();
	openExternal.mockResolvedValue(undefined);
});

test("iOS button opens the App Store listing through the app bridge", async () => {
	render(<ConnectMobileGetApp />);
	await userEvent.click(screen.getByRole("button", { name: "Open Agent Orchestrator on the App Store" }));
	expect(openExternal).toHaveBeenCalledWith("https://apps.apple.com/app/ao-mobile/id6792552173");
	expect(IOS_APP_STORE_URL).toBe("https://apps.apple.com/app/ao-mobile/id6792552173");
});

// A storefront-pinned link ("/us/") 404s for every visitor outside that
// country; the bare /app/ form redirects to the viewer's own storefront.
test("App Store link is not pinned to a storefront", () => {
	expect(IOS_APP_STORE_URL).not.toContain("/us/");
});

test("Android button opens the Google Play listing", async () => {
	render(<ConnectMobileGetApp />);
	expect(screen.getByText("Android")).toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Open Agent Orchestrator on Google Play" }));
	expect(openExternal).toHaveBeenCalledWith("https://play.google.com/store/apps/details?id=aoagents.dev&pcampaignid=web_share");
	expect(ANDROID_PLAY_STORE_URL).toBe("https://play.google.com/store/apps/details?id=aoagents.dev&pcampaignid=web_share");
});

// Both stores are a public one-tap listing now, so the iOS row says the same
// thing the Android row does rather than explaining a prerequisite install.
test("iOS row points straight at the App Store", () => {
	render(<ConnectMobileGetApp />);
	expect(screen.getByText(/Get Agent Orchestrator on the App Store/i)).toBeInTheDocument();
});

test("QR code stays collapsed until the disclosure is toggled", async () => {
	render(<ConnectMobileGetApp />);
	const toggle = screen.getByRole("button", { name: "Show App Store QR code" });
	expect(toggle).toHaveAttribute("aria-expanded", "false");
	expect(screen.getByTestId("ios-store-qr")).toHaveAttribute("aria-hidden", "true");

	await userEvent.click(toggle);

	expect(screen.getByRole("button", { name: "Hide App Store QR code" })).toHaveAttribute("aria-expanded", "true");
	expect(screen.getByTestId("ios-store-qr")).toHaveAttribute("aria-hidden", "false");
});
