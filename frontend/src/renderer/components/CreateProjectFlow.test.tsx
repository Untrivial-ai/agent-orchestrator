import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CreateProjectFlow, type CloneProjectInput, type CreateProjectInput } from "./CreateProjectFlow";

const bridgeMocks = vi.hoisted(() => ({
	checkAncestorRepo: vi.fn(),
	chooseDirectory: vi.fn(),
	getRepositoryBranch: vi.fn(),
	scanImportFolder: vi.fn(),
}));

const apiMocks = vi.hoisted(() => ({
	POST: vi.fn(),
	apiErrorMessage: vi.fn((error: unknown, fallback = "Request failed") =>
		typeof error === "object" && error !== null && "message" in error ? String((error as { message?: unknown }).message) : fallback,
	),
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		app: {
			checkAncestorRepo: bridgeMocks.checkAncestorRepo,
			chooseDirectory: bridgeMocks.chooseDirectory,
			getRepositoryBranch: bridgeMocks.getRepositoryBranch,
			scanImportFolder: bridgeMocks.scanImportFolder,
		},
	},
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		POST: apiMocks.POST,
	},
	apiErrorMessage: apiMocks.apiErrorMessage,
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
	useCloudGate: () => ({
		cloudEnabled: cloudMocks.cloudEnabled,
		localEnabled: true,
		client: "",
	}),
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
		onSubmit,
		open,
		path,
	}: {
		kind: string;
		onSubmit: (selection: { workerAgent: string; orchestratorAgent: string }) => Promise<void>;
		open: boolean;
		path: string | null;
	}) =>
		open ? (
			<div data-kind={kind} data-path={path ?? ""} data-testid="agent-sheet">
				<button type="button" onClick={() => void onSubmit({ workerAgent: "codex", orchestratorAgent: "codex" })}>
					Submit agents
				</button>
			</div>
		) : null,
}));

// Probe stand-in: the real dialog needs its own form state and validation.
// These tests only care whether the clone flow is on screen and that the
// droppedPath guard leaves it alone, so a thin stub keeps the suite focused.
vi.mock("./CloneRepositoryDialog", () => ({
	default: ({
		onContinue,
		open,
	}: {
		onContinue: (selection: { remoteUrl: string; destinationParent: string; targetPath: string }) => void;
		open: boolean;
	}) =>
		open ? (
			<div data-testid="clone-dialog">
				<button
					type="button"
					onClick={() =>
						onContinue({
							remoteUrl: "https://github.com/acme/project.git",
							destinationParent: "/repos",
							targetPath: "/repos/project",
						})
					}
				>
					Continue clone
				</button>
			</div>
		) : null,
	repositoryAvatarFromGitUrl: () => null,
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

function projectValidation(
	path: string,
	overrides: Partial<{
		isValid: boolean;
		blockingErrors: string[];
		nextStep: "error" | "choose_import_kind" | "prepare_git" | "continue";
		root: Partial<{
			repoPath: string;
			isRepo: boolean;
			hasCommit: boolean;
			hasOrigin: boolean;
			isEmptyFolder: boolean;
			needsGitInit: boolean;
			requiredActions: string[];
			blockingErrors: string[];
		}>;
		childRepos: Array<{
			repoPath: string;
			isRepo: boolean;
			hasCommit: boolean;
			hasOrigin: boolean;
			isEmptyFolder: boolean;
			needsGitInit: boolean;
			requiredActions: string[];
			blockingErrors: string[];
		}>;
		warning: string;
	}> = {},
) {
	return {
		importKind: "project",
		isValid: overrides.isValid ?? true,
		blockingErrors: overrides.blockingErrors ?? [],
		root: {
			repoPath: overrides.root?.repoPath ?? path,
			isRepo: overrides.root?.isRepo ?? true,
			hasCommit: overrides.root?.hasCommit ?? true,
			hasOrigin: overrides.root?.hasOrigin ?? true,
			isEmptyFolder: overrides.root?.isEmptyFolder ?? false,
			needsGitInit: overrides.root?.needsGitInit ?? false,
			requiredActions: overrides.root?.requiredActions ?? [],
			blockingErrors: overrides.root?.blockingErrors ?? [],
		},
		childRepos: overrides.childRepos,
		nextStep: overrides.nextStep ?? "continue",
		warning: overrides.warning,
	};
}

function workspaceValidation(
	path: string,
	children: ReturnType<typeof projectValidation>["root"][],
	nextStep: "error" | "choose_import_kind" | "prepare_git" | "continue" = "continue",
) {
	return {
		...projectValidation(path, { nextStep }),
		importKind: "workspace",
		root: {
			...projectValidation(path).root,
			isRepo: false,
			hasCommit: false,
			hasOrigin: false,
			needsGitInit: true,
			requiredActions: ["git_init", "git_commit", "set_remote"],
		},
		childRepos: children,
	};
}

beforeEach(() => {
	bridgeMocks.checkAncestorRepo.mockReset().mockResolvedValue(undefined);
	bridgeMocks.chooseDirectory.mockReset();
	bridgeMocks.getRepositoryBranch.mockReset().mockResolvedValue(undefined);
	bridgeMocks.scanImportFolder.mockReset().mockImplementation(async ({ path }: { path: string }) => okScan(path));
	apiMocks.POST.mockReset();
	apiMocks.apiErrorMessage.mockClear();
	cloudMocks.cloudEnabled = false;
	cloudMocks.sessionStatus = "unauthenticated";
	cloudMocks.createProject.mockReset();
	cloudMocks.signIn.mockReset();
	window.localStorage.clear();
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
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/dropped/proj"),
		});
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} />);
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} />);

		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		await waitFor(() =>
			expect(apiMocks.POST).toHaveBeenCalledWith("/api/v1/imports/validate", {
				body: { importKind: "project", path: "/dropped/proj" },
			}),
		);
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/dropped/proj");
		expect(sheet).toHaveAttribute("data-kind", "single_repo");
	});

	it("does not let a stale dropped path leak into the next manual New Project click", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/manually/chosen");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/manually/chosen"),
		});
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} openSignal={0} />);

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
			expect(apiMocks.POST).toHaveBeenCalledWith("/api/v1/imports/validate", {
				body: { importKind: "project", path: "/manually/chosen" },
			}),
		);
	});

	it("retains the selected folder and agent sheet after an unrelated create failure", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockRejectedValueOnce(new Error("AO daemon is not ready.")).mockResolvedValueOnce(undefined);
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/dropped/project") });
		const { rerender } = render(
			<CreateProjectFlow mode="choose" {...noop} onCreateProject={onCreateProject} droppedPath={null} />,
		);
		rerender(<CreateProjectFlow mode="choose" {...noop} onCreateProject={onCreateProject} droppedPath={{ nonce: 1, path: "/dropped/project" }} />);
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(screen.getByTestId("agent-sheet")).toHaveAttribute("data-path", "/dropped/project");
		await user.click(screen.getByRole("button", { name: "Submit agents" }));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(2));
		await waitFor(() => expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument());
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
	});

	it("ignores a drop while the agent sheet is already open", async () => {
		const user = userEvent.setup();
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/dropped/first"),
		});
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
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} openSignal={0} />);

		// Open the mode picker manually and switch to the clone flow.
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={null} openSignal={1} />);
		await user.click(await screen.findByRole("button", { name: "Clone from Git" }));
		expect(await screen.findByTestId("clone-dialog")).toBeInTheDocument();

		// A folder is dropped while the clone dialog is on screen.
		rerender(<CreateProjectFlow mode="choose" {...noop} droppedPath={{ nonce: 1, path: "/dropped/proj" }} openSignal={1} />);

		expect(screen.getByTestId("clone-dialog")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Import an existing project" })).not.toBeInTheDocument();
		expect(bridgeMocks.chooseDirectory).not.toHaveBeenCalled();
	});
});

describe("CreateProjectFlow project import validation", () => {
	it("shows validation failure before agent selection", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/bad-project");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/bad-project", {
				isValid: false,
				blockingErrors: ["INVALID_PATH"],
				nextStep: "error",
			}),
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		expect(await screen.findByText("Choose a folder AO can read.")).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Back to import source" }));
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
	});

	it("suggests workspace import when a plain root contains child repositories", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/parent");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/parent", {
				nextStep: "choose_import_kind",
				root: {
					isRepo: false,
					hasCommit: false,
					hasOrigin: false,
					needsGitInit: true,
					requiredActions: ["git_init", "git_commit", "set_remote"],
				},
				childRepos: [
					{
						repoPath: "/repo/parent/web",
						isRepo: true,
						hasCommit: true,
						hasOrigin: true,
						isEmptyFolder: false,
						needsGitInit: false,
						requiredActions: [],
						blockingErrors: [],
					},
				],
			}),
		}).mockResolvedValueOnce({
			data: workspaceValidation("/repo/parent", [
				{
					repoPath: "/repo/parent/web",
					isRepo: true,
					hasCommit: true,
					hasOrigin: true,
					isEmptyFolder: false,
					needsGitInit: false,
					requiredActions: [],
					blockingErrors: [],
				},
			]),
		});
		bridgeMocks.scanImportFolder.mockResolvedValueOnce({
			path: "/repo/parent",
			repos: [
				{
					branch: "main",
					hasRemote: true,
					name: "web",
					path: "/repo/parent/web",
					relativePath: "web",
					remote: "https://github.com/acme/web.git",
					status: "ok",
				},
			],
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		expect(await screen.findByText("This folder contains child Git repos")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Clone from Git" })).not.toBeInTheDocument();
		await user.click(
			await screen.findByRole("button", {
				name: "Import as workspace instead",
			}),
		);
		expect(await screen.findByText("Import workspace")).toBeInTheDocument();
		expect(screen.getByText("web")).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
	});

	it("shows only the missing Git preparation steps for a project root", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: {
					hasCommit: false,
					hasOrigin: false,
					requiredActions: ["git_commit", "set_remote"],
				},
			}),
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		expect(await screen.findByText("Prepare project")).toBeInTheDocument();
		expect(screen.getByText("Project setup")).toBeInTheDocument();
		expect(screen.queryByText("Git initialization")).not.toBeInTheDocument();
		expect(screen.getByText("Initial commit")).toBeInTheDocument();
		expect(screen.getByText("Remote setup")).toBeInTheDocument();
		expect(screen.queryByText("Create the first commit so the project has a usable history.")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Origin remote URL")).toBeInTheDocument();
		expect(
			screen.getByText(
				"To create sessions and PRs successfully, make sure this repository also exists on GitHub and that you can push the default branch to it.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Plain folder")).not.toBeInTheDocument();
		expect(screen.queryByText("No commit yet")).not.toBeInTheDocument();
		expect(screen.queryByText("No origin remote")).not.toBeInTheDocument();
	});

	it("prefills a default GitHub remote URL for the selected project", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project-no-git");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project-no-git", {
				nextStep: "prepare_git",
				root: {
					hasOrigin: false,
					requiredActions: ["set_remote"],
				},
			}),
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		expect(await screen.findByLabelText("Origin remote URL")).toHaveValue("https://github.com/username/project-no-git.git");
	});

	it("requires the user to keep all required setup actions approved", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: {
					hasOrigin: false,
					requiredActions: ["set_remote"],
				},
			}),
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));

		const remoteAction = screen.getByRole("checkbox");
		expect(remoteAction).toBeChecked();
		await user.click(screen.getByLabelText("Origin remote URL"));
		expect(remoteAction).toBeChecked();
		await user.click(remoteAction);

		expect(screen.getByText("Approve all required setup actions to continue importing this project.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
	});

	it("prepares the project and then opens agent selection", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: {
						isRepo: false,
						hasCommit: false,
						hasOrigin: false,
						needsGitInit: true,
						requiredActions: ["git_init", "git_commit", "set_remote"],
					},
				}),
		}).mockResolvedValueOnce({
				data: {
					events: [
						{ repoPath: "/repo/project", action: "git_init", state: "pending" },
						{ repoPath: "/repo/project", action: "git_init", state: "running" },
						{ repoPath: "/repo/project", action: "git_init", state: "success" },
						{ repoPath: "/repo/project", action: "git_commit", state: "pending" },
						{ repoPath: "/repo/project", action: "git_commit", state: "running" },
						{ repoPath: "/repo/project", action: "git_commit", state: "success" },
						{ repoPath: "/repo/project", action: "set_remote", state: "pending" },
						{ repoPath: "/repo/project", action: "set_remote", state: "running" },
						{ repoPath: "/repo/project", action: "set_remote", state: "success" },
					],
					validation: projectValidation("/repo/project"),
				},
			});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));
		const remoteInput = await screen.findByLabelText("Origin remote URL");
		await user.clear(remoteInput);
		await user.type(remoteInput, "https://github.com/acme/project.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));

		await waitFor(() =>
			expect(apiMocks.POST).toHaveBeenLastCalledWith("/api/v1/imports/prepare-git", {
				body: {
					importKind: "project",
					path: "/repo/project",
					approvedActions: ["git_init", "git_commit", "set_remote"],
					remoteUrl: "https://github.com/acme/project.git",
				},
			}),
		);
		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/repo/project");
		expect(screen.queryByText("Prepare project")).not.toBeInTheDocument();
	});

	it.each(["single_repo", "workspace"] as const)("passes the checked-out root branch when importing %s", async (kind) => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn(async () => undefined);
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		bridgeMocks.getRepositoryBranch.mockResolvedValue("main");
		apiMocks.POST.mockResolvedValueOnce({
			data:
				kind === "workspace"
					? workspaceValidation("/repo/project", [projectValidation("/repo/project/child").root])
					: projectValidation("/repo/project"),
		});

		render(
			<CreateProjectFlow mode={kind} {...noop} onCreateProject={onCreateProject}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		if (kind === "workspace") await user.click(await screen.findByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repo/project",
				asWorkspace: kind === "workspace",
				defaultBranch: "main",
				workerAgent: "codex",
				orchestratorAgent: "codex",
			}),
		);
		expect(bridgeMocks.getRepositoryBranch).toHaveBeenCalledWith("/repo/project");
	});

	it("shows queued and running setup progress after continue is clicked", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		let resolvePrepare!: (value: unknown) => void;
		apiMocks.POST.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: {
						isRepo: false,
						hasCommit: false,
						hasOrigin: false,
						needsGitInit: true,
						requiredActions: ["git_init", "git_commit", "set_remote"],
					},
				}),
		}).mockReturnValueOnce(
				new Promise((resolve) => {
					resolvePrepare = resolve;
				}),
			);

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));
		const remoteInput = await screen.findByLabelText("Origin remote URL");
		await user.clear(remoteInput);
		await user.type(remoteInput, "https://github.com/acme/project.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByText("Running project setup. AO is preparing this repository now.")).toBeInTheDocument();
		expect(screen.queryByText("Running")).not.toBeInTheDocument();
		expect(screen.queryByText("Queued")).not.toBeInTheDocument();
		expect(screen.queryByText("Ready")).not.toBeInTheDocument();
		expect(screen.queryByText("Set URL")).not.toBeInTheDocument();

		resolvePrepare({
			data: {
				events: [
					{ repoPath: "/repo/project", action: "git_init", state: "success" },
					{ repoPath: "/repo/project", action: "git_commit", state: "success" },
					{ repoPath: "/repo/project", action: "set_remote", state: "success" },
				],
				validation: projectValidation("/repo/project"),
			},
		});

		expect(await screen.findByTestId("agent-sheet")).toHaveAttribute("data-path", "/repo/project");
	});

	it("shows a failed preparation step and allows retry", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: {
						hasOrigin: false,
						requiredActions: ["set_remote"],
					},
				}),
		}).mockResolvedValueOnce({
				data: {
					events: [
						{ repoPath: "/repo/project", action: "set_remote", state: "pending" },
						{ repoPath: "/repo/project", action: "set_remote", state: "running" },
					{
						repoPath: "/repo/project",
						action: "set_remote",
						state: "error",
						error: "origin exists",
					},
					],
					validation: projectValidation("/repo/project", {
						nextStep: "prepare_git",
						root: {
							hasOrigin: false,
							requiredActions: ["set_remote"],
						},
					}),
				},
			});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import an existing project" }));
		const remoteInput = await screen.findByLabelText("Origin remote URL");
		await user.clear(remoteInput);
		await user.type(remoteInput, "https://github.com/acme/project.git");
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByText(/failed while running Remote setup/i)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
	});
});

describe("CreateProjectFlow clone and workspace transitions", () => {
	it("prepares a clone before agent setup and registers the prepared checkout", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn(async () => undefined);
		const onCloneProject = vi.fn(async () => undefined);
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				path: "/repos/project",
				remoteUrl: "https://github.com/acme/project.git",
			},
		}).mockResolvedValueOnce({ data: projectValidation("/repos/project") });

		render(
			<CreateProjectFlow mode="choose" {...noop} onCloneProject={onCloneProject} onCreateProject={onCreateProject}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Clone from Git" }));
		await user.click(await screen.findByRole("button", { name: "Continue clone" }));

		expect(await screen.findByText("Preparing repository")).toBeInTheDocument();
		expect(apiMocks.POST).toHaveBeenNthCalledWith(1, "/api/v1/projects/clone/prepare", {
			body: {
				remoteUrl: "https://github.com/acme/project.git",
				destinationParent: "/repos",
			},
		});
		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/repos/project");

		await user.click(screen.getByRole("button", { name: "Submit agents" }));
		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repos/project",
				workerAgent: "codex",
				orchestratorAgent: "codex",
			}),
		);
		expect(onCloneProject).not.toHaveBeenCalled();
	});

	it("automatically prepares the first commit for an empty cloned repository", async () => {
		const user = userEvent.setup();
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				path: "/repos/empty",
				remoteUrl: "https://github.com/acme/project.git",
			},
		})
			.mockResolvedValueOnce({
				data: projectValidation("/repos/empty", {
					nextStep: "prepare_git",
					root: { hasCommit: false, requiredActions: ["git_commit"] },
				}),
			})
			.mockResolvedValueOnce({
				data: {
					events: [
						{
							repoPath: "/repos/empty",
							action: "git_commit",
							state: "success",
						},
					],
					validation: projectValidation("/repos/empty"),
				},
			});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);
		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Clone from Git" }));
		await user.click(await screen.findByRole("button", { name: "Continue clone" }));

		await waitFor(() =>
			expect(apiMocks.POST).toHaveBeenNthCalledWith(3, "/api/v1/imports/prepare-git", {
				body: {
					importKind: "project",
					path: "/repos/empty",
					approvedActions: ["git_commit"],
					remoteUrl: undefined,
					initialCommitMessage: "Initial commit",
				},
			}),
		);
		expect(await screen.findByTestId("agent-sheet")).toHaveAttribute("data-path", "/repos/empty");
		expect(screen.queryByText("Prepare project")).not.toBeInTheDocument();
	});

	it("reviews a valid workspace before agent setup and uses workspace creation progress", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn(() => new Promise<void>(() => undefined));
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/workspace",
			repos: [
				{
					branch: "main",
					hasRemote: true,
					name: "web",
					path: "/repo/workspace/web",
					relativePath: "web",
					remote: "https://github.com/acme/web.git",
					status: "ok",
				},
			],
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: workspaceValidation("/repo/workspace", [
				{
					repoPath: "/repo/workspace/web",
					isRepo: true,
					hasCommit: true,
					hasOrigin: true,
					isEmptyFolder: false,
					needsGitInit: false,
					requiredActions: [],
					blockingErrors: [],
				},
			]),
		});

		render(
			<CreateProjectFlow mode="choose" {...noop} onCreateProject={onCreateProject}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);
		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import a workspace folder" }));
		expect(await screen.findByText("web")).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));

		expect(await screen.findByText("Creating workspace")).toBeInTheDocument();
		expect(screen.getByRole("progressbar")).toBeInTheDocument();
		expect(onCreateProject).toHaveBeenCalledWith({
			path: "/repo/workspace",
			asWorkspace: true,
			workerAgent: "codex",
			orchestratorAgent: "codex",
		});
	});

	it("repairs a workspace child from the compact row before agent setup", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/workspace",
			repos: [
				{
					branch: "main",
					hasRemote: false,
					name: "api",
					path: "/repo/workspace/api",
					relativePath: "api",
					remote: "",
					status: "error",
					reason: "Repository does not have an origin remote.",
				},
			],
		});
		const child = {
			repoPath: "/repo/workspace/api",
			isRepo: true,
			hasCommit: true,
			hasOrigin: false,
			isEmptyFolder: false,
			needsGitInit: false,
			requiredActions: ["set_remote"],
			blockingErrors: [],
		};
		apiMocks.POST.mockResolvedValueOnce({
			data: workspaceValidation("/repo/workspace", [child], "prepare_git"),
		}).mockResolvedValueOnce({
			data: {
				events: [{ repoPath: child.repoPath, action: "set_remote", state: "success" }],
				validation: workspaceValidation("/repo/workspace", [{ ...child, hasOrigin: true, requiredActions: [] }]),
			},
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);
		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import a workspace folder" }));
		await user.click(await screen.findByRole("button", { name: "No remote · Set up" }));
		const remote = screen.getByLabelText("Origin remote URL");
		await user.clear(remote);
		await user.type(remote, "https://github.com/acme/api.git");
		await user.click(screen.getByRole("button", { name: "Apply setup" }));

		await waitFor(() =>
			expect(apiMocks.POST).toHaveBeenLastCalledWith("/api/v1/imports/prepare-git", {
				body: {
					importKind: "workspace",
					path: "/repo/workspace",
					repositories: [
						{
							repoPath: "/repo/workspace/api",
							approvedActions: ["set_remote"],
							remoteUrl: "https://github.com/acme/api.git",
						},
					],
				},
			}),
		);
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(await screen.findByTestId("agent-sheet")).toHaveAttribute("data-kind", "workspace");
	});

	it("allows a workspace with one ready child to continue without repairing the others", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/workspace",
			repos: [
				{
					branch: "main",
					hasRemote: true,
					name: "web",
					path: "/repo/workspace/web",
					relativePath: "web",
					remote: "https://github.com/example/web.git",
					status: "ok",
				},
				{
					branch: "main",
					hasRemote: false,
					name: "api",
					path: "/repo/workspace/api",
					relativePath: "api",
					remote: "",
					status: "error",
					reason: "Repository does not have an origin remote.",
				},
			],
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: workspaceValidation(
				"/repo/workspace",
				[
					projectValidation("/repo/workspace/web").root,
					{
						...projectValidation("/repo/workspace/api").root,
						hasOrigin: false,
						requiredActions: ["set_remote"],
					},
				],
				"prepare_git",
			),
		});

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);
		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "Import a workspace folder" }));
		expect(await screen.findByRole("button", { name: "No remote · Set up" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByTestId("agent-sheet")).toHaveAttribute("data-kind", "workspace");
		expect(apiMocks.POST).toHaveBeenCalledTimes(1);
	});
});

describe("CreateProjectFlow cloud offering", () => {
	it("hides the Local | Cloud choice when the cloud gate is off", () => {
		cloudMocks.sessionStatus = "authenticated";
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, {
			wrapper: CloudTestProviders,
		});

		expect(screen.queryByRole("tab", { name: "Cloud" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
	});

	it("shows the Cloud choice and sign-in prompt when the user is signed out", async () => {
		cloudMocks.cloudEnabled = true;
		const user = userEvent.setup();
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, {
			wrapper: CloudTestProviders,
		});

		expect(screen.getByRole("tab", { name: "Local", selected: true })).toBeInTheDocument();
		await user.click(screen.getByRole("tab", { name: "Cloud" }));
		expect(screen.getByText(/sign in to AO Cloud to create a cloud project/i)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Sign in to AO Cloud" }));
		expect(cloudMocks.signIn).toHaveBeenCalledOnce();
	});

	it("shows the choice defaulting to Local when the gate is on and the user is signed in", () => {
		cloudMocks.cloudEnabled = true;
		cloudMocks.sessionStatus = "authenticated";
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, {
			wrapper: CloudTestProviders,
		});

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
		render(<CreateProjectFlow embedded mode="choose" {...noop} />, {
			wrapper: CloudTestProviders,
		});

		await user.click(screen.getByRole("tab", { name: "Cloud" }));
		await user.type(screen.getByLabelText("Repository URL"), "git@github.com:acme/web-app.git");
		await user.type(screen.getByLabelText("Project name"), "web-app");
		await user.click(screen.getByRole("button", { name: "Create cloud project" }));

		expect(await screen.findByText("Enter an https repository URL.")).toBeInTheDocument();
		expect(cloudMocks.createProject).not.toHaveBeenCalled();
	});
});
