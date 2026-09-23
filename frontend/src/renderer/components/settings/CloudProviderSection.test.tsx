import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "../../i18n";
import { useSandboxProviderStore } from "../../stores/sandbox-provider-store";

const gate = vi.hoisted(() => ({ cloudEnabled: true }));
const sessionStatus = vi.hoisted(() => ({ status: "authenticated" as string }));
const providers = vi.hoisted(() => ({ available: ["nodeops", "coder"], default: "nodeops" }));

vi.mock("../../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: gate.cloudEnabled, localEnabled: true, client: "" }),
}));
vi.mock("../../lib/cloud-session", () => ({
	useCloudSession: () => ({ status: sessionStatus.status }),
}));
vi.mock("../../hooks/useCloudSandboxProviders", () => ({
	useCloudSandboxProviders: () => ({
		available: providers.available,
		default: providers.default,
		ready: true,
		isLoading: false,
	}),
}));

import { CloudProviderSection } from "./CloudProviderSection";

describe("CloudProviderSection", () => {
	beforeEach(() => {
		gate.cloudEnabled = true;
		sessionStatus.status = "authenticated";
		providers.available = ["nodeops", "coder"];
		providers.default = "nodeops";
		window.localStorage.clear();
		useSandboxProviderStore.setState({ selectedProvider: null });
	});

	it("renders nothing when the cloud offering is disabled", () => {
		gate.cloudEnabled = false;
		const { container } = render(<CloudProviderSection />);
		expect(container).toBeEmptyDOMElement();
	});

	it("prompts sign-in when not authenticated", () => {
		sessionStatus.status = "signedOut";
		render(<CloudProviderSection />);
		expect(screen.getByText(/sign in to ao cloud/i)).toBeInTheDocument();
	});

	it("shows a selector defaulting to the control plane default provider", () => {
		render(<CloudProviderSection />);
		// The section renders, and the trigger shows the default-marked provider.
		expect(screen.getByTestId("settings-section")).toHaveAttribute("data-section", "cloud-provider");
		expect(screen.getByText("NodeOps (default)")).toBeInTheDocument();
	});

	it("reflects the persisted selection over the default", () => {
		useSandboxProviderStore.setState({ selectedProvider: "coder" });
		render(<CloudProviderSection />);
		expect(screen.getByText("Coder")).toBeInTheDocument();
	});

	it("shows the single provider read-only when only one is offered", () => {
		providers.available = ["coder"];
		providers.default = "coder";
		render(<CloudProviderSection />);
		expect(screen.getByText("Coder")).toBeInTheDocument();
		// No selectable menu trigger when there is nothing to choose.
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});
});
