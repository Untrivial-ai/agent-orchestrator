import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CreateProjectFlow, type CloneProjectInput, type CreateProjectInput } from "./CreateProjectFlow";

const bridgeMocks = vi.hoisted(() => ({
	checkAncestorRepo: vi.fn(),
	chooseDirectory: vi.fn(),
	getGitHubLogin: vi.fn(),
	getRepositoryBranch: vi.fn(),
	scanImportFolder: vi.fn(),
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		app: {
			checkAncestorRepo: bridgeMocks.checkAncestorRepo,
			chooseDirectory: bridgeMocks.chooseDirectory,
			getGitHubLogin: bridgeMocks.getGitHubLogin,
			getRepositoryBranch: bridgeMocks.getRepositoryBranch,
			scanImportFolder: bridgeMocks.scanImportFolder,
		},
	},
}));

// Cloud stand-ins: the flow only consumes the gate flag, the session status,
// and the typed client's createProject; everything else stays out of scope.
const cloudMocks = vi.hoisted(() => ({
	cloudEnabled: false,
	sessionStatus: "unauthenticated",
	createProject: vi.fn(),
	signIn: vi.fn(),
}));

vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: cloudMocks.cloudEnabled, localEnabled: true, client: "" }),
}));

vi.mock("../lib/cloud-session", () => ({
	useCloudSession: () => ({
		configured: true,
		session: null,
		status: cloudMocks.sessionStatus,
		signIn: cloudMocks.signIn,
		signOut: async () => undefined,
	}),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { createProject: cloudMocks.createProject },
		ready: cloudMocks.cloudEnabled && cloudMocks.sessionStatus === "authenticated",
		baseUrl: "https://cp.example.com",
	}),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({
		org: { id: "org-1", slug: "acme", displayName: "Acme", role: "admin" },
		isLoading: false,
		error: undefined,
		ready: true,
	}),
}));

// The cloud form invalidates the workspace query via useQueryClient, so cloud
// tests render inside a provider. Local-only tests don't need one.
function CloudTestProviders({ children }: { children: ReactNode }) {
	const [queryClient] = useState(() => new QueryClient());
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

// Probe stand-in: the real sheet needs a QueryClientProvider + agent catalog to
// render. These tests only care which path/kind CreateProjectFlow hands it and
// whether it's open, so a thin stub keeps the suite fast and focused.
vi.mock("./CreateProjectAgentSheet", () => ({
	CreateProjectAgentSheet: ({
		kind,
		open,
		path,
	}: {
		kind: string;
		open: boolean;
		path: string | null;
	}) => (open ? <div data-kind={kind} data-path={path ?? ""} data-testid="agent-sheet" /> : null),
}));

// Probe stand-in: the real dialog needs its own form state and validation.
// These tests only care whether the clone flow is on screen and that the
// droppedPath guard leaves it alone, so a thin stub keeps the suite focused.
vi.mock("./CloneRepositoryDialog", () => ({
	default: ({ open }: { open: boolean }) => (open ? <div data-testid="clone-dialog" /> : null),
}));

function okScan(path: string) {
	return {
		path,
		repos: [
			{
				branch: "main",
				hasRemote: true,
				name: "proj",
				path,
				relativePath: ".",
				remote: "git@github.com:example/proj.git",
				status: "ok" as const,
			},
		],
	};
}

const noop = {
	onCloneProject: async (_input: CloneProjectInput) => undefined,
	onCreateProject: async (_input: CreateProjectInput) => undefined,
	onInitializeProject: async (_path: string) => undefined,
};

beforeEach(() => {
	bridgeMocks.checkAncestorRepo.mockReset().mockResolvedValue(undefined);
	bridgeMocks.chooseDirectory.mockReset();
	bridgeMocks.getGitHubLogin.mockReset().mockResolvedValue("test-user");
	bridgeMocks.getRepositoryBranch.mockReset().mockResolvedValue(undefined);
	bridgeMocks.scanImportFolder.mockReset().mockImplementation(async ({ path }: { path: string }) => okScan(path));
	cloudMocks.cloudEnabled = false;
	cloudMocks.sessionStatus = "unauthenticated";
	cloudMocks.createProject.mockReset();
	cloudMocks.signIn.mockReset();
});

describe("CreateProjectFlow droppedPath", () => {
	it("does not open on mount", () => {
		render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} />);
		expect(screen.queryByRole("button", { name: "Import a workspace folder" })).not.toBeInTheDocument();
	});

	it("opens the mode picker without invoking the native folder chooser", async () => {
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} />);

		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} />);

		expect(await screen.findByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
	});

	it("uses the dropped path for preflight and opens the agent sheet, skipping the native dialog", async () => {
		const user = userEvent.setup();
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} />);
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} />);

		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		await waitFor(() =>
			expect(bridgeMocks.scanImportFolder).toHaveBeenCalledWith({ mode: "project", path: "/dropped/proj" }),
		);
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/dropped/proj");
		expect(sheet).toHaveAttribute("data-kind", "single_repo");
	});

	it("does not let a stale dropped path leak into the next manual New Project click", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/manually/chosen");
		const { rerender } = render(
			<CreateProjectFlow mode="choose" {...noop} droppedPath={null} openSignal={0} />,
		);

		// Drop a folder, then dismiss the mode picker without picking a kind.
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} openSignal={0} />);
		await user.click(await screen.findByRole("button", { name: "Close new project dialog" }));
		await waitFor(() => expect(screen.queryByRole("button", { name: "Import an existing project" })).not.toBeInTheDocument());

		// A manual "New Project" (⌘N-style openSignal bump) must fall back to the
		// native dialog, not silently reuse the dismissed drop's path.
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} openSignal={1} />);
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		await waitFor(() => expect(bridgeMocks.chooseDirectory).toHaveBeenCalledTimes(1));
		await waitFor(() =>
			expect(bridgeMocks.scanImportFolder).toHaveBeenCalledWith({ mode: "project", path: "/manually/chosen" }),
		);
	});

	it("ignores a drop while the agent sheet is already open", async () => {
		const user = userEvent.setup();
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} />);
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/first" }} />);
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));
		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/dropped/first");

		// A second, different folder is dropped while the agent sheet is open.
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 2, path: "/dropped/second" }} />);

		expect(screen.getByTestId("agent-sheet")).toHaveAttribute("data-path", "/dropped/first");
		expect(screen.queryByRole("button", { name: "Import an existing project" })).not.toBeInTheDocument();
	});

	it("ignores a drop while the clone-from-Git dialog is open", async () => {
		const user = userEvent.setup();
		const { rerender } = render(
			<CreateProjectFlow mode="choose" {...noop} droppedPath={null} openSignal={0} />,
		);

		// Open the mode picker manually and switch to the clone flow.
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} openSignal={1} />);
		await user.click(await screen.findByRole("button", { name: "Clone from Git" }));
		expect(await screen.findByTestId("clone-dialog")).toBeInTheDocument();

		// A folder is dropped while the clone dialog is on screen.
		rerender(
			<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} openSignal={1} />,
		);

		expect(screen.getByTestId("clone-dialog")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Import an existing project" })).not.toBeInTheDocument();
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
	});
});

describe("CreateProjectFlow cloud offering", () => {
	it("hides the Local | Cloud choice when the cloud gate is off", () => {
		cloudMocks.sessionStatus = "authenticated";
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, { wrapper: CloudTestProviders });

		expect(screen.queryByRole("tab", { name: "Cloud" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
	});

	it("shows the Cloud choice and sign-in prompt when the user is signed out", async () => {
		cloudMocks.cloudEnabled = true;
		const user = userEvent.setup();
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, { wrapper: CloudTestProviders });

		expect(screen.getByRole("tab", { name: "Local", selected: true })).toBeInTheDocument();
		await user.click(screen.getByRole("tab", { name: "Cloud" }));
		expect(screen.getByText(/sign in to AO Cloud to create a cloud project/i)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Sign in to AO Cloud" }));
		expect(cloudMocks.signIn).toHaveBeenCalledOnce();
	});

	it("shows the choice defaulting to Local when the gate is on and the user is signed in", () => {
		cloudMocks.cloudEnabled = true;
		cloudMocks.sessionStatus = "authenticated";
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, { wrapper: CloudTestProviders });

		expect(screen.getByRole("tab", { name: "Local", selected: true })).toBeInTheDocument();
		expect(screen.getByRole("tab", { name: "Cloud", selected: false })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
	});

	it("creates a cloud project through the control-plane client instead of the daemon flow", async () => {
		cloudMocks.cloudEnabled = true;
		cloudMocks.sessionStatus = "authenticated";
		cloudMocks.createProject.mockResolvedValue({ project: { id: "cp-1" } });
		const onCreateProject = vi.fn();
		const user = userEvent.setup();
		render(<CreateProjectFlow embedded mode="choose" {...noop} onCreateProject={onCreateProject} />, {
			wrapper: CloudTestProviders,
		});

		await user.click(screen.getByRole("tab", { name: "Cloud" }));
		await user.type(screen.getByLabelText("Repository URL"), "https://github.com/acme/web-app");
		await user.type(screen.getByLabelText("Project name"), "web-app");
		await user.click(screen.getByRole("button", { name: "Create cloud project" }));

		await waitFor(() =>
			expect(cloudMocks.createProject).toHaveBeenCalledWith("org-1", {
				displayName: "web-app",
				repositoryUrl: "https://github.com/acme/web-app",
				defaultBranch: "main",
			}),
		);
		expect(onCreateProject).not.toHaveBeenCalled();
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
	});

	it("blocks a non-https repository URL without calling the control plane", async () => {
		cloudMocks.cloudEnabled = true;
		cloudMocks.sessionStatus = "authenticated";
		const user = userEvent.setup();
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, { wrapper: CloudTestProviders });

		await user.click(screen.getByRole("tab", { name: "Cloud" }));
		await user.type(screen.getByLabelText("Repository URL"), "git@github.com:acme/web-app.git");
		await user.type(screen.getByLabelText("Project name"), "web-app");
		await user.click(screen.getByRole("button", { name: "Create cloud project" }));

		expect(await screen.findByText("Enter an https repository URL.")).toBeInTheDocument();
		expect(cloudMocks.createProject).not.toHaveBeenCalled();
	});
});
