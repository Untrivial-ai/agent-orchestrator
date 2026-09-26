import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

const flowMocks = vi.hoisted(() => ({
	triggers: [] as Array<{ kind: string; nonce: number } | undefined>,
}));
const cloudMocks = vi.hoisted(() => ({
	cloudEnabled: false,
	signIn: vi.fn(),
	status: "unauthenticated" as "authenticated" | "unauthenticated",
}));

// The step owns two rows and nothing else. Picking a folder, validating it, and
// preparing a repository that still needs git both belong to the create project
// flow, so the contract here is the trigger it gets handed.
vi.mock("./CreateProjectFlow", () => ({
	CloudProjectCard: () => <div data-testid="cloud-project-card" />,
	CloudSignInPanel: () => <div data-testid="cloud-sign-in-panel" />,
	CreateProjectFlow: (props: { onboardingTrigger?: { kind: string; nonce: number } }) => {
		flowMocks.triggers.push(props.onboardingTrigger);
		return null;
	},
}));

vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ client: "", cloudEnabled: cloudMocks.cloudEnabled, localEnabled: true }),
}));

vi.mock("../lib/cloud-session", () => ({
	useCloudSession: () => ({ signIn: cloudMocks.signIn, status: cloudMocks.status }),
}));

import { OnboardingProjectSetup } from "./OnboardingProjectSetup";

function renderStep() {
	render(
		<OnboardingProjectSetup
			onCloudProjectCreated={vi.fn()}
			onPrepared={vi.fn()}
		/>,
	);
}

beforeEach(() => {
	cloudMocks.cloudEnabled = false;
	cloudMocks.status = "unauthenticated";
	flowMocks.triggers = [];
});

it("hands the folder row to the flow, which owns the picker and any git preparation", async () => {
	renderStep();

	await userEvent.click(screen.getByRole("button", { name: "Import an existing project" }));

	expect(flowMocks.triggers.at(-1)).toEqual({ kind: "folder", nonce: 1 });
});

it("hands the clone row to the flow", async () => {
	renderStep();

	await userEvent.click(screen.getByRole("button", { name: "Clone from Git" }));

	expect(flowMocks.triggers.at(-1)).toEqual({ kind: "clone", nonce: 1 });
});

it("re-triggers the flow when the same row is picked again", async () => {
	renderStep();
	const clone = screen.getByRole("button", { name: "Clone from Git" });

	await userEvent.click(clone);
	await userEvent.click(clone);

	expect(flowMocks.triggers.at(-1)).toEqual({ kind: "clone", nonce: 2 });
});

it("offers cloud as a project source once the cloud step enabled it", async () => {
	cloudMocks.cloudEnabled = true;
	renderStep();

	expect(screen.queryByTestId("cloud-sign-in-panel")).not.toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Create a cloud project" }));
	expect(await screen.findByTestId("cloud-sign-in-panel")).toBeInTheDocument();
});

it("goes straight to the cloud project form when the account is signed in", async () => {
	cloudMocks.cloudEnabled = true;
	cloudMocks.status = "authenticated";
	renderStep();

	await userEvent.click(screen.getByRole("button", { name: "Create a cloud project" }));
	expect(await screen.findByTestId("cloud-project-card")).toBeInTheDocument();
});
