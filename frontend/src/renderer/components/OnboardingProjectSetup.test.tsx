import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

const bridgeMocks = vi.hoisted(() => ({ chooseDirectory: vi.fn(), getRepositoryBranch: vi.fn() }));
const apiMocks = vi.hoisted(() => ({ POST: vi.fn() }));
const cloudMocks = vi.hoisted(() => ({
	cloudEnabled: false,
	signIn: vi.fn(),
	status: "unauthenticated" as "authenticated" | "unauthenticated",
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: { app: { chooseDirectory: bridgeMocks.chooseDirectory, getRepositoryBranch: bridgeMocks.getRepositoryBranch } },
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: apiMocks.POST },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("./CreateProjectFlow", () => ({
	CloudProjectCard: () => <div data-testid="cloud-project-card" />,
	CloudSignInPanel: () => <div data-testid="cloud-sign-in-panel" />,
	CreateProjectFlow: () => null,
}));

vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ client: "", cloudEnabled: cloudMocks.cloudEnabled, localEnabled: true }),
}));

vi.mock("../lib/cloud-session", () => ({
	useCloudSession: () => ({ signIn: cloudMocks.signIn, status: cloudMocks.status }),
}));

import { OnboardingProjectSetup } from "./OnboardingProjectSetup";

beforeEach(() => {
	cloudMocks.cloudEnabled = false;
	cloudMocks.status = "unauthenticated";
	bridgeMocks.chooseDirectory.mockReset();
	bridgeMocks.getRepositoryBranch.mockReset().mockResolvedValue("main");
	apiMocks.POST.mockReset().mockResolvedValue({
		data: {
			isValid: true,
			nextStep: "continue",
			root: { isRepo: true, hasCommit: true },
		},
	});
});

it("offers cloud as a project source once the cloud step enabled it", async () => {
	cloudMocks.cloudEnabled = true;
	const user = userEvent.setup();

	render(
		<OnboardingProjectSetup
			mode="folder"
			onCloudProjectCreated={vi.fn()}
			onModeChange={vi.fn()}
			onPrepared={vi.fn()}
			preparedProject={null}
		/>,
	);

	expect(screen.queryByTestId("cloud-sign-in-panel")).not.toBeInTheDocument();
	await user.click(screen.getByRole("button", { name: "Create a cloud project" }));
	expect(await screen.findByTestId("cloud-sign-in-panel")).toBeInTheDocument();
});

it("goes straight to the cloud project form when the account is signed in", async () => {
	cloudMocks.cloudEnabled = true;
	cloudMocks.status = "authenticated";
	const user = userEvent.setup();

	render(
		<OnboardingProjectSetup
			mode="folder"
			onCloudProjectCreated={vi.fn()}
			onModeChange={vi.fn()}
			onPrepared={vi.fn()}
			preparedProject={null}
		/>,
	);

	await user.click(screen.getByRole("button", { name: "Create a cloud project" }));
	expect(await screen.findByTestId("cloud-project-card")).toBeInTheDocument();
});

it("advances with the folder returned by the native picker", async () => {
	bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
	const onPrepared = vi.fn();

	render(
		<OnboardingProjectSetup
			mode="folder"
			onModeChange={vi.fn()}
			onCloudProjectCreated={vi.fn()}
			onPrepared={onPrepared}
			preparedProject={null}
		/>,
	);

	await userEvent.click(screen.getByRole("button", { name: "Open local folder" }));

	await waitFor(() => expect(onPrepared).toHaveBeenLastCalledWith({
		path: "/repo/project",
		defaultBranch: "main",
		repositorySetup: null,
	}));
});

it("carries initialization requirements forward for a plain folder", async () => {
	bridgeMocks.chooseDirectory.mockResolvedValue("/repo/plain");
	apiMocks.POST.mockResolvedValueOnce({
		data: {
			isValid: true,
			nextStep: "prepare_git",
			root: { isRepo: false, hasCommit: false },
		},
	});
	const onPrepared = vi.fn();

	render(
		<OnboardingProjectSetup
			mode="folder"
			onModeChange={vi.fn()}
			onCloudProjectCreated={vi.fn()}
			onPrepared={onPrepared}
			preparedProject={null}
		/>,
	);

	await userEvent.click(screen.getByRole("button", { name: "Open local folder" }));

	await waitFor(() => expect(onPrepared).toHaveBeenLastCalledWith(expect.objectContaining({
		path: "/repo/plain",
		repositorySetup: "NOT_A_GIT_REPO",
	})));
});
