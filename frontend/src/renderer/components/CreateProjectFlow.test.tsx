import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { useState, type ComponentProps, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CreateProjectFlow, type CloneProjectInput, type CreateProjectInput } from "./CreateProjectFlow";
import { useUiStore } from "../stores/ui-store";

const bridgeMocks = vi.hoisted(() => ({
	checkAncestorRepo: vi.fn(),
	checkGitRepository: vi.fn(),
	checkGitHubRepositoryAvailability: vi.fn(),
	chooseDirectory: vi.fn(),
	getGitHubLogin: vi.fn(),
	getCachedGitHubOwners: vi.fn(),
	refreshGitHubOwners: vi.fn(),
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
			checkGitRepository: bridgeMocks.checkGitRepository,
			checkGitHubRepositoryAvailability: bridgeMocks.checkGitHubRepositoryAvailability,
		chooseDirectory: bridgeMocks.chooseDirectory,
		getGitHubLogin: bridgeMocks.getGitHubLogin,
		getCachedGitHubOwners: bridgeMocks.getCachedGitHubOwners,
		refreshGitHubOwners: bridgeMocks.refreshGitHubOwners,
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
		error,
		kind,
		onSubmit,
		open,
		path,
		shake,
	}: {
		error?: string | null;
		kind: string;
		onSubmit: (selection: { workerAgent: string; orchestratorAgent: string }) => Promise<void>;
		open: boolean;
		path: string | null;
		shake?: boolean;
	}) =>
		open ? (
			<div className={shake ? "modal-shake" : undefined} data-kind={kind} data-path={path ?? ""} data-testid="agent-sheet">
				{error ? <span>{error}</span> : null}
				<button
					type="button"
					onClick={() => void onSubmit({ workerAgent: "codex", orchestratorAgent: "codex" })}
				>
					Submit agents
				</button>
			</div>
		) : null,
}));

// Probe stand-in: the real dialog needs its own form state and validation.
// These tests only care whether the clone flow is on screen and that the
// droppedPath guard leaves it alone, so a thin stub keeps the suite focused.
vi.mock("./CloneRepositoryDialog", () => ({
	default: ({ open, onBack, onChange, onClose, onContinue, value }: {
		onBack?: () => void;
		onChange?: (value: { remoteUrl: string; destinationParent: string }) => void;
		onClose?: () => void;
		onContinue?: (selection: { remoteUrl: string; destinationParent: string; targetPath: string }) => void;
		open: boolean;
		value: { remoteUrl: string; destinationParent: string };
	}) =>
		open ? (
			<div data-testid="clone-dialog" data-destination={value.destinationParent}>
				<input
					aria-label="Clone URL"
					value={value.remoteUrl}
					onChange={(event) => onChange?.({ ...value, remoteUrl: event.target.value })}
				/>
				<button type="button" onClick={onBack}>Back clone</button>
				<button type="button" onClick={onClose}>Close clone</button>
				<button type="button" onClick={() => onContinue?.({ remoteUrl: "file:///source/empty-repository.git", destinationParent: "/repo", targetPath: "/repo/empty-repository" })}>
					Continue clone
				</button>
			</div>
		) : null,
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

function renderChooseFlow(overrides: Partial<ComponentProps<typeof CreateProjectFlow>> = {}) {
	return render(
		<CreateProjectFlow mode="choose" {...noop} {...overrides}>
			{({ choosePath }) => <button onClick={choosePath}>New project</button>}
		</CreateProjectFlow>,
	);
}

async function openSource(user: ReturnType<typeof userEvent.setup>, name: string) {
	await user.click(screen.getByRole("button", { name: "New project" }));
	await user.click(await screen.findByRole("button", { name }));
}

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

beforeEach(() => {
	bridgeMocks.checkAncestorRepo.mockReset().mockResolvedValue(undefined);
	bridgeMocks.checkGitRepository.mockReset().mockResolvedValue(true);
	bridgeMocks.checkGitHubRepositoryAvailability.mockReset().mockResolvedValue({ available: true });
	bridgeMocks.chooseDirectory.mockReset();
	bridgeMocks.getGitHubLogin.mockReset().mockResolvedValue("");
	bridgeMocks.getCachedGitHubOwners.mockReset().mockResolvedValue([{ login: "username", avatarUrl: "https://avatars.example/username" }, { login: "acme", avatarUrl: "https://avatars.example/acme" }]);
	bridgeMocks.refreshGitHubOwners.mockReset().mockResolvedValue([{ login: "username", avatarUrl: "https://avatars.example/username" }, { login: "acme", avatarUrl: "https://avatars.example/acme" }]);
	bridgeMocks.getRepositoryBranch.mockReset().mockResolvedValue(undefined);
	bridgeMocks.scanImportFolder.mockReset().mockImplementation(async ({ path }: { path: string }) => okScan(path));
	apiMocks.POST.mockReset();
	apiMocks.apiErrorMessage.mockClear();
	cloudMocks.cloudEnabled = false;
	cloudMocks.sessionStatus = "unauthenticated";
	cloudMocks.createProject.mockReset();
	cloudMocks.signIn.mockReset();
	window.localStorage.clear();
	useUiStore.setState({ globalToast: null, globalToasts: [] });
});

describe("CreateProjectFlow droppedPath", () => {
	it("shows the standalone agent action when the host provides one", async () => {
		const onCreateStandaloneAgent = vi.fn();
		const user = userEvent.setup();
		renderChooseFlow({ onCreateStandaloneAgent });

		await user.click(screen.getByRole("button", { name: "New project" }));
		await user.click(await screen.findByRole("button", { name: "New standalone agent" }));

		expect(onCreateStandaloneAgent).toHaveBeenCalledOnce();
		await waitFor(() => expect(screen.queryByRole("button", { name: "New standalone agent" })).not.toBeInTheDocument());
	});

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
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/dropped/proj") });
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
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/manually/chosen") });
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
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/dropped/first") });
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

	it.each([null, "/chosen/projects"])("uses a sensible clone destination with saved folder %s", async (saved) => {
		window.localStorage.removeItem("ao.clone.lastDestinationParent");
		if (saved) window.localStorage.setItem("ao.clone.lastDestinationParent", saved);
		const user = userEvent.setup();
		const { rerender } = render(<CreateProjectFlow mode="choose" {...noop} openSignal={0} />);
		rerender(<CreateProjectFlow mode="choose" {...noop} openSignal={1} />);
		await user.click(await screen.findByRole("button", { name: "Clone from Git" }));
		expect(await screen.findByTestId("clone-dialog")).toHaveAttribute("data-destination", saved ?? "~/ao/projects");
		window.localStorage.removeItem("ao.clone.lastDestinationParent");
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

	it("routes an empty clone through Prepare project", async () => {
		const user = userEvent.setup();
		apiMocks.POST
			.mockResolvedValueOnce({ data: { path: "/repo/empty-repository", remoteUrl: "file:///source/empty-repository.git", preparationId: "prep-empty" } })
			.mockResolvedValueOnce({
				data: projectValidation("/repo/empty-repository", {
					nextStep: "prepare_git",
					root: { requiredActions: ["git_commit", "set_remote"], hasCommit: false, hasOrigin: false },
				}),
			});

		renderChooseFlow();
		await openSource(user, "Clone from Git");
		fireEvent.click(await screen.findByText("Continue clone"));

		expect(await screen.findByText("Prepare project")).toBeInTheDocument();
		expect(apiMocks.POST).toHaveBeenNthCalledWith(1, "/api/v1/projects/clone/prepare", expect.anything());
		expect(apiMocks.POST).toHaveBeenNthCalledWith(2, "/api/v1/imports/validate", {
			body: { importKind: "project", path: "/repo/empty-repository" },
		});
	});

	it("keeps the clone dialog visible until preparation is ready", async () => {
		const user = userEvent.setup();
		let resolveClone!: (value: unknown) => void;
		let resolveValidation!: (value: unknown) => void;
		apiMocks.POST.mockImplementation((path: string) => {
			if (path === "/api/v1/projects/clone/prepare") {
				return new Promise((resolve) => {
					resolveClone = resolve;
				});
			}
			return new Promise((resolve) => {
				resolveValidation = resolve;
			});
		});

		renderChooseFlow();
		await openSource(user, "Clone from Git");
		fireEvent.click(await screen.findByText("Continue clone"));
		expect(screen.getByTestId("clone-dialog")).toBeInTheDocument();

		resolveClone({ data: { path: "/repo/empty-repository", remoteUrl: "file:///source/empty-repository.git", preparationId: "prep-empty" } });
		await waitFor(() => expect(apiMocks.POST).toHaveBeenCalledWith("/api/v1/imports/validate", expect.anything()));
		expect(screen.getByTestId("clone-dialog")).toBeInTheDocument();
		resolveValidation({ data: projectValidation("/repo/empty-repository", { nextStep: "prepare_git" }) });
		expect(await screen.findByText("Prepare project")).toBeInTheDocument();
		expect(screen.queryByTestId("clone-dialog")).not.toBeInTheDocument();
	});

	it("cleans up a checkout when validation fails after cloning", async () => {
		const user = userEvent.setup();
		apiMocks.POST.mockImplementation(async (path: string) => {
			if (path === "/api/v1/projects/clone/prepare") {
				return { data: { path: "/repo/incomplete", remoteUrl: "file:///source/incomplete.git", preparationId: "prep-incomplete" } };
			}
			if (path === "/api/v1/imports/validate") {
				return { error: { message: "rpc failed: request_id=secret" } };
			}
			return {};
		});

		renderChooseFlow();
		await openSource(user, "Clone from Git");
		fireEvent.click(await screen.findByText("Continue clone"));

		await waitFor(() => expect(apiMocks.POST).toHaveBeenCalledWith(
			"/api/v1/projects/clone/cleanup",
			{ body: { path: "/repo/incomplete", preparationId: "prep-incomplete" } },
		));
		expect(screen.getByTestId("clone-dialog")).toBeInTheDocument();
		expect(useUiStore.getState().globalToast?.body).toBe(
			"AO cloned the repository but could not verify the checkout. Try again.",
		);
		expect(useUiStore.getState().globalToast?.body).not.toContain("request_id");
	});

	it("keeps a failed checkout cleanup retryable before leaving clone", async () => {
		const user = userEvent.setup();
		let cleanupAttempts = 0;
		apiMocks.POST.mockImplementation(async (path: string) => {
			if (path === "/api/v1/projects/clone/prepare") {
				return { data: { path: "/repo/incomplete", remoteUrl: "file:///source/incomplete.git", preparationId: "prep-incomplete" } };
			}
			if (path === "/api/v1/imports/validate") return { error: { message: "validation unavailable" } };
			if (path === "/api/v1/projects/clone/cleanup") {
				cleanupAttempts += 1;
				return cleanupAttempts === 1 ? { error: { message: "permission denied" } } : {};
			}
			return {};
		});

		renderChooseFlow();
		await openSource(user, "Clone from Git");
		fireEvent.click(await screen.findByText("Continue clone"));

		await waitFor(() => expect(cleanupAttempts).toBe(1));
		expect(screen.getByTestId("clone-dialog")).toBeInTheDocument();
		expect(useUiStore.getState().globalToast?.body).toBe(
			"AO could not remove the incomplete checkout. Try again before leaving this flow.",
		);

		fireEvent.click(screen.getByText("Back clone"));
		await waitFor(() => expect(cleanupAttempts).toBe(2));
		expect(await screen.findByRole("button", { name: "Clone from Git" })).toBeInTheDocument();
		expect(screen.queryByTestId("clone-dialog")).not.toBeInTheDocument();
	});

	it("starts clone details fresh each time it opens", async () => {
		const user = userEvent.setup();
		renderChooseFlow();
		await openSource(user, "Clone from Git");
		fireEvent.change(await screen.findByLabelText("Clone URL"), { target: { value: "https://example.com/old.git" } });
		fireEvent.click(screen.getByText("Back clone"));
		fireEvent.click(await screen.findByRole("button", { name: "Clone from Git" }));

		expect(await screen.findByLabelText("Clone URL")).toHaveValue("");
	});

	it("keeps clone progress open without offering cancellation", async () => {
		const user = userEvent.setup();
		let finishCreate!: () => void;
		const onCreateProject = vi.fn(() => new Promise<void>((resolve) => {
			finishCreate = resolve;
		}));
		apiMocks.POST
			.mockResolvedValueOnce({ data: { path: "/repo/cloned", remoteUrl: "file:///source/cloned.git", preparationId: "prep-cloned" } })
			.mockResolvedValueOnce({ data: projectValidation("/repo/cloned") });

		renderChooseFlow({ onCreateProject });
		await openSource(user, "Clone from Git");
		fireEvent.click(await screen.findByText("Continue clone"));
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));
		expect(onCreateProject).toHaveBeenCalledWith(expect.objectContaining({
			path: "/repo/cloned",
			clonePreparationId: "prep-cloned",
		}));
		expect(await screen.findByRole("dialog", { name: "Creating the project" })).toBeInTheDocument();

		expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
		fireEvent.keyDown(document, { key: "Escape" });
		expect(screen.getByRole("dialog", { name: "Creating the project" })).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();

		await act(async () => finishCreate());
		await waitFor(() => expect(screen.queryByRole("dialog", { name: "Creating the project" })).not.toBeInTheDocument());
	});
});

describe("CreateProjectFlow project import validation", () => {
	it("opens a registered project before validation or agent selection", async () => {
		const user = userEvent.setup();
		const onOpenExistingProject = vi.fn();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/existing/");

		render(
			<CreateProjectFlow
				mode="choose"
				{...noop}
				existingProjectPaths={["/repo/existing"]}
				onOpenExistingProject={onOpenExistingProject}
			>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await openSource(user, "Import an existing project");

		await waitFor(() => expect(onOpenExistingProject).toHaveBeenCalledWith("/repo/existing"));
		expect(apiMocks.POST).not.toHaveBeenCalled();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
		expect(useUiStore.getState().globalToasts).toHaveLength(1);
		expect(useUiStore.getState().globalToast).toMatchObject({
			title: "Project already added",
			body: "Opened the registered project for this folder.",
		});
	});

	it("offers to import a repository selected as a workspace as a project", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST
			.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					// Root repository classification is authoritative even if an older
					// daemon omits the explicit choose-import-kind transition.
					nextStep: "continue",
					warning: "This folder is already a Git project. AO will import it as a project instead of a workspace.",
				}),
			})
			.mockResolvedValueOnce({ data: projectValidation("/repo/project") });

		renderChooseFlow();

		await openSource(user, "Import a workspace folder");

		expect(await screen.findByText("This is a single repository, not a collection of repositories. Import it as a project instead.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import as project" })).toBeInTheDocument();
		expect(screen.queryByText("proj")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Import as project" }));
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
		expect(screen.queryByText("Choose a project folder")).not.toBeInTheDocument();

		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/repo/project");
		expect(sheet).toHaveAttribute("data-kind", "single_repo");
		expect(screen.queryByRole("dialog", { name: "Import workspace" })).not.toBeInTheDocument();
		expect(apiMocks.POST).toHaveBeenNthCalledWith(2, "/api/v1/imports/validate", {
			body: { importKind: "project", path: "/repo/project" },
		});
	});

	it("keeps workspace import available when the parent repository has no remote", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/workspace",
			repos: [{
				name: "app",
				path: "/repo/workspace/app",
				relativePath: "app",
				branch: "main",
				remote: "https://github.com/acme/app.git",
				hasRemote: true,
				isRepo: true,
				hasCommit: true,
				status: "ok",
				needsGitInit: false,
			}],
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				...projectValidation("/repo/workspace", {
					nextStep: "continue",
					root: { isRepo: true, hasCommit: true, hasOrigin: false, requiredActions: ["create_remote_repository"] },
					childRepos: [{
						repoPath: "/repo/workspace/app",
						isRepo: true,
						hasCommit: true,
						hasOrigin: true,
						isEmptyFolder: false,
						needsGitInit: false,
						requiredActions: [],
						blockingErrors: [],
					}],
				}),
				importKind: "workspace",
			},
		});

		renderChooseFlow();
		await openSource(user, "Import a workspace folder");

		expect(screen.queryByText("This is a single repository, not a collection of repositories. Import it as a project instead.")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Import as project" })).not.toBeInTheDocument();
		expect(await screen.findByText("app")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByTestId("agent-sheet")).toHaveAttribute("data-kind", "workspace");
		expect(screen.getByTestId("agent-sheet")).toHaveAttribute("data-path", "/repo/workspace");
	});

	it("blocks workspace import when a child repository has no remote", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/workspace",
			repos: [{
				name: "app",
				path: "/repo/workspace/app",
				relativePath: "app",
				branch: "main",
				remote: "",
				hasRemote: false,
				isRepo: true,
				hasCommit: true,
				status: "ok",
				needsGitInit: false,
			}],
		});
		apiMocks.POST.mockResolvedValueOnce({
				data: {
					...projectValidation("/repo/workspace", {
						nextStep: "prepare_git",
						root: { isRepo: false, hasCommit: false, hasOrigin: false, needsGitInit: true, requiredActions: [] },
						childRepos: [{
							repoPath: "/repo/workspace/app",
							isRepo: true,
							hasCommit: true,
							hasOrigin: false,
							isEmptyFolder: false,
							needsGitInit: false,
							requiredActions: ["set_remote"],
							blockingErrors: [],
						}],
					}),
					importKind: "workspace",
				},
			});

		renderChooseFlow();
		await openSource(user, "Import a workspace folder");
		expect(screen.getByText("Set an origin remote for the child repositories marked below before importing this workspace.")).toBeInTheDocument();
		expect(screen.getByText("app")).toBeInTheDocument();
		expect(screen.getByText("Setup required")).toBeInTheDocument();
		expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
		expect(screen.queryByRole("textbox", { name: "Origin remote URL" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
		expect(apiMocks.POST).toHaveBeenCalledTimes(1);
	});

	it("disables workspace import when no child Git repositories exist", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({ path: "/repo/workspace", repos: [] });
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				...projectValidation("/repo/workspace", {
					isValid: false,
					blockingErrors: ["WORKSPACE_CHILD_REPO_REQUIRED"],
					nextStep: "error",
					root: { isRepo: false, hasCommit: false, hasOrigin: false, needsGitInit: true, blockingErrors: ["WORKSPACE_CHILD_REPO_REQUIRED"] },
				}),
				importKind: "workspace",
			},
		});

		renderChooseFlow();
		await openSource(user, "Import a workspace folder");

		expect(screen.getByText("Importing a workspace requires at least one direct child Git repository that already has a commit and an origin remote. You can import this folder as a project instead.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Continue" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import as project" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
		expect(useUiStore.getState().globalToast).toBeNull();
		expect(screen.getByRole("dialog", { name: "Import workspace" })).not.toHaveClass("modal-shake");
	});

	it("keeps workspace import disabled when only non-Git child folders exist", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/workspace",
			repos: [{ name: "empty", path: "/repo/workspace/empty", relativePath: "empty", branch: "", remote: "", hasRemote: false, isRepo: false, hasCommit: false, status: "ok", needsGitInit: true }],
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				...projectValidation("/repo/workspace", {
					isValid: false,
					blockingErrors: ["WORKSPACE_CHILD_REPO_REQUIRED"],
					nextStep: "error",
					root: { isRepo: false, hasCommit: false, hasOrigin: false, needsGitInit: true, blockingErrors: ["WORKSPACE_CHILD_REPO_REQUIRED"] },
					childRepos: [],
				}),
				importKind: "workspace",
			},
		});

		renderChooseFlow();
		await openSource(user, "Import a workspace folder");

		expect(screen.getByText("Importing a workspace requires at least one direct child Git repository that already has a commit and an origin remote. You can import this folder as a project instead.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Continue" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import as project" })).toBeInTheDocument();
		expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
	});

	it("blocks invalid workspace validation with a toast and modal shake", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/workspace");
		bridgeMocks.scanImportFolder.mockResolvedValue({ path: "/repo/workspace", repos: [] });
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				...projectValidation("/repo/workspace", {
					isValid: false,
					blockingErrors: ["UNSUPPORTED_GIT_METADATA"],
					nextStep: "error",
					root: { blockingErrors: ["UNSUPPORTED_GIT_METADATA"] },
				}),
				importKind: "workspace",
			},
		});

		renderChooseFlow();
		await openSource(user, "Import a workspace folder");

		expect(useUiStore.getState().globalToast?.body).toBe("Repair the Git metadata or choose a different folder.");
		await waitFor(() => expect(screen.getByRole("dialog", { name: "Import workspace" })).toHaveClass("modal-shake"));
		expect(screen.queryByRole("button", { name: "Continue" })).not.toBeInTheDocument();
		expect(screen.queryByText("Import failed · workspace not registered")).not.toBeInTheDocument();
	});

	it("uses one shared backdrop while switching between flow modals", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/repo/project", { nextStep: "prepare_git" }) });

		renderChooseFlow();

		await openSource(user, "Import an existing project");
		await screen.findByText("Prepare project");

		expect(document.querySelectorAll(".dialog-overlay")).toHaveLength(1);
	});

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

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		await waitFor(() => expect(useUiStore.getState().globalToast?.body).toBe("Choose a folder AO can read."));
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Back to import source" }));
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
	});

	it("requires plain roots with child repositories to be imported as workspaces", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/parent");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/parent",
			repos: [{ name: "web", path: "/repo/parent/web", relativePath: "web", branch: "main", remote: "https://example.com/web.git", hasRemote: true, isRepo: true, hasCommit: true, status: "ok", needsGitInit: false }],
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/parent", {
				nextStep: "choose_import_kind",
				root: {
					isRepo: false,
					hasCommit: false,
					hasOrigin: false,
					needsGitInit: true,
					requiredActions: ["git_init", "git_commit", "create_remote_repository"],
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
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/parent", {
				root: { isRepo: false, hasCommit: false, hasOrigin: false, needsGitInit: true, requiredActions: ["git_init", "git_commit", "create_remote_repository"] },
				childRepos: [{ repoPath: "/repo/parent/web", isRepo: true, hasCommit: true, hasOrigin: true, isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [] }],
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		expect(await screen.findByText("This folder contains child Git repositories. Import it as a workspace instead.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Continue" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Back" })).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Import as workspace" }));
		expect(await screen.findByRole("dialog", { name: "Import workspace" })).toBeInTheDocument();
		expect(apiMocks.POST).toHaveBeenNthCalledWith(2, "/api/v1/imports/validate", {
			body: { importKind: "workspace", path: "/repo/parent" },
		});
	});

	it("groups all required Git preparation behind one approval", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: {
					hasCommit: false,
					hasOrigin: false,
					requiredActions: ["git_commit", "create_remote_repository"],
				},
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		expect(await screen.findByText("Prepare project")).toBeInTheDocument();
		expect(screen.queryByText("Project setup")).not.toBeInTheDocument();
		expect(screen.getByText("project")).toBeInTheDocument();
		expect(screen.getByText("does not have a GitHub remote. AO will create a repository, add it as origin, and push the current branch.")).toBeInTheDocument();
		expect(screen.queryByRole("checkbox", { name: "Set up Git for this project" })).not.toBeInTheDocument();
		expect(screen.queryByText("Git initialization")).not.toBeInTheDocument();
		expect(screen.queryByText("Initial commit")).not.toBeInTheDocument();
		expect(screen.queryByText("Remote setup")).not.toBeInTheDocument();
		expect(screen.queryByText("Create the first commit so the project has a usable history.")).not.toBeInTheDocument();
		await waitFor(() => expect(screen.getByLabelText("Owner")).toHaveTextContent("username"));
		expect(screen.getByLabelText("Repository name")).toHaveValue("project");
			expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeDisabled();
		expect(screen.queryByText("Plain folder")).not.toBeInTheDocument();
		expect(screen.queryByText("No commit yet")).not.toBeInTheDocument();
		expect(screen.queryByText("No origin remote")).not.toBeInTheDocument();
	});

	it("shows a project-with-child-repos warning before agent selection", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				warning: "This folder contains child Git repositories and will be imported as one project.",
				childRepos: [{
					repoPath: "/repo/project/child",
					isRepo: true,
					hasCommit: true,
					hasOrigin: true,
					isEmptyFolder: false,
					needsGitInit: false,
					requiredActions: [],
					blockingErrors: [],
				}],
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		expect(await screen.findByText("This folder contains child Git repositories and will be imported as one project.")).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByTestId("agent-sheet")).toHaveAttribute("data-path", "/repo/project");
		expect(apiMocks.POST).toHaveBeenCalledTimes(1);
	});

	it("prefills a default GitHub remote URL for the selected project", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project-no-git");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project-no-git", {
				nextStep: "prepare_git",
				root: {
					hasOrigin: false,
					requiredActions: ["create_remote_repository"],
				},
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		await waitFor(() => expect(screen.getByLabelText("Owner")).toHaveTextContent("username"));
		expect(screen.getByLabelText("Repository name")).toHaveValue("project-no-git");
			expect(screen.queryByText(/Will create/)).not.toBeInTheDocument();
		expect(screen.queryByText("Repository name is available.")).not.toBeInTheDocument();
	});

	it("shows Other after selecting a custom GitHub owner", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project-no-git");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project-no-git", {
				nextStep: "prepare_git",
				root: {
					hasOrigin: false,
					requiredActions: ["create_remote_repository"],
				},
			}),
		});

		renderChooseFlow();
		await openSource(user, "Import an existing project");

		await user.click(await screen.findByLabelText("Owner"));
		await user.click(await screen.findByRole("option", { name: "Use a different owner" }));

		expect(screen.getByText("Other")).toBeInTheDocument();
		expect(screen.getByRole("textbox", { name: "Owner" })).toHaveValue("username");
	});

	it("requires an available GitHub repository name", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		bridgeMocks.checkGitHubRepositoryAvailability.mockResolvedValue({ available: false, message: "Repository name is already in use for this owner." });
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: {
					hasOrigin: false,
					requiredActions: ["create_remote_repository"],
				},
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		expect(await screen.findByRole("alert")).toHaveTextContent("Repository name is already in use for this owner.");
			expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeDisabled();
	});

	it("checks repository availability again when the repository name changes", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		bridgeMocks.checkGitHubRepositoryAvailability
			.mockResolvedValueOnce({ available: false, message: "Repository name is already in use for this owner." })
			.mockResolvedValueOnce({ available: true });
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: {
					hasOrigin: false,
					requiredActions: ["create_remote_repository"],
				},
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		expect(await screen.findByText("Repository name is already in use for this owner.")).toBeInTheDocument();
		const repoNameInput = screen.getByLabelText("Repository name");
		await user.clear(repoNameInput);
		await user.type(repoNameInput, "project-new");

		await waitFor(() => expect(bridgeMocks.checkGitHubRepositoryAvailability).toHaveBeenLastCalledWith({ owner: "username", name: "project-new" }));
		expect(screen.getByText("project")).toBeInTheDocument();
		expect(screen.queryByText("Repository name is available.")).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeEnabled();
	});

	it("prepares the project and then opens agent selection", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST
			.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: {
						isRepo: false,
						hasCommit: false,
						hasOrigin: false,
						needsGitInit: true,
						requiredActions: ["git_init", "git_commit", "create_remote_repository"],
					},
				}),
			})
			.mockResolvedValueOnce({
				data: {
					events: [
						{ repoPath: "/repo/project", action: "git_init", state: "pending" },
						{ repoPath: "/repo/project", action: "git_init", state: "running" },
						{ repoPath: "/repo/project", action: "git_init", state: "success" },
					],
					validation: projectValidation("/repo/project", {
						nextStep: "prepare_git",
						root: { isRepo: true, hasCommit: false, hasOrigin: false, requiredActions: ["git_commit", "create_remote_repository"] },
					}),
				},
			})
			.mockResolvedValueOnce({
				data: {
					events: [
						{ repoPath: "/repo/project", action: "git_commit", state: "pending" },
						{ repoPath: "/repo/project", action: "git_commit", state: "running" },
						{ repoPath: "/repo/project", action: "git_commit", state: "success" },
					],
					validation: projectValidation("/repo/project", {
						nextStep: "prepare_git",
						root: { isRepo: true, hasCommit: true, hasOrigin: false, requiredActions: ["create_remote_repository"] },
					}),
				},
			})
			.mockResolvedValueOnce({
				data: {
					events: [
						{ repoPath: "/repo/project", action: "create_remote_repository", state: "pending" },
						{ repoPath: "/repo/project", action: "create_remote_repository", state: "running" },
						{ repoPath: "/repo/project", action: "create_remote_repository", state: "success" },
					],
					validation: projectValidation("/repo/project"),
				},
			});

		renderChooseFlow();

		await openSource(user, "Import an existing project");
		const ownerInput = await screen.findByLabelText("Owner");
		await user.click(ownerInput);
		await user.click(await screen.findByRole("option", { name: "acme" }));
		const privateRepository = screen.getByRole("switch", { name: "Private repository" });
		expect(privateRepository).toBeChecked();
		expect(screen.getByText("Private repository")).toBeInTheDocument();
		expect(screen.getByText("Only you and people you invite can see this repo")).toBeInTheDocument();
		await user.click(privateRepository);
		expect(privateRepository).not.toBeChecked();
		expect(screen.getByRole("switch", { name: "Public repository" })).toBe(privateRepository);
		expect(screen.getByText("Public repository")).toBeInTheDocument();
		expect(screen.getByText("Anyone on the internet can see this repo")).toBeInTheDocument();
			await waitFor(() => expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeEnabled());
			await user.click(screen.getByRole("button", { name: "Create repository and continue" }));

		await waitFor(() =>
			expect(apiMocks.POST).toHaveBeenLastCalledWith("/api/v1/imports/prepare-git", {
				body: {
					importKind: "project",
					path: "/repo/project",
					approvedActions: ["git_init", "git_commit", "create_remote_repository"],
					remoteUrl: "https://github.com/acme/project.git",
					githubRepository: { owner: "acme", name: "project", private: false },
					stepwise: true,
				},
			}),
		);
		expect(apiMocks.POST).toHaveBeenCalledTimes(4);
		const sheet = await screen.findByTestId("agent-sheet");
		expect(sheet).toHaveAttribute("data-path", "/repo/project");
		expect(screen.queryByText("Prepare project")).not.toBeInTheDocument();
	});

	it("updates visibility toggle label, helper text, and accessible name dynamically when toggled", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: { hasOrigin: false, requiredActions: ["create_remote_repository"] },
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");
		const ownerInput = await screen.findByLabelText("Owner");
		await user.click(ownerInput);
		await user.click(await screen.findByRole("option", { name: "acme" }));

		// Default state is ON (Private repository)
		const toggle = screen.getByRole("switch", { name: "Private repository" });
		expect(toggle).toBeChecked();
		expect(screen.getByText("Private repository")).toBeInTheDocument();
		expect(screen.getByText("Only you and people you invite can see this repo")).toBeInTheDocument();

		// Toggle to OFF (Public repository)
		await user.click(toggle);
		expect(toggle).not.toBeChecked();
		expect(screen.getByRole("switch", { name: "Public repository" })).toBe(toggle);
		expect(screen.getByText("Public repository")).toBeInTheDocument();
		expect(screen.getByText("Anyone on the internet can see this repo")).toBeInTheDocument();

		// Toggle back to ON (Private repository)
		await user.click(toggle);
		expect(toggle).toBeChecked();
		expect(screen.getByRole("switch", { name: "Private repository" })).toBe(toggle);
		expect(screen.getByText("Private repository")).toBeInTheDocument();
		expect(screen.getByText("Only you and people you invite can see this repo")).toBeInTheDocument();
	});

	it("blocks an unavailable GitHub repository before Git preparation", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		bridgeMocks.checkGitHubRepositoryAvailability.mockResolvedValue({ available: false, message: "Repository name is already in use for this owner." });
		apiMocks.POST.mockResolvedValueOnce({
			data: projectValidation("/repo/project", {
				nextStep: "prepare_git",
				root: { hasOrigin: false, requiredActions: ["create_remote_repository"] },
			}),
		});

		renderChooseFlow();

		await openSource(user, "Import an existing project");

		await waitFor(() => expect(bridgeMocks.checkGitHubRepositoryAvailability).toHaveBeenCalledWith({ owner: "username", name: "project" }));
		expect(apiMocks.POST).toHaveBeenCalledTimes(1);
		expect(screen.getByText("Repository name is already in use for this owner.")).toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeDisabled();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();
	});

	it("toasts and shakes the agent sheet when project creation fails", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/repo/project") });
		const onCreateProject = vi.fn().mockRejectedValue(new Error("rpc failed: request_id=secret INTERNAL_FAILURE"));

		render(
			<CreateProjectFlow mode="choose" {...noop} onCreateProject={onCreateProject}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await openSource(user, "Import an existing project");
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));

		await waitFor(() => expect(useUiStore.getState().globalToast?.body).toBe("AO could not create this project. Try again."));
		const sheet = screen.getByTestId("agent-sheet");
		expect(sheet).toHaveTextContent("AO could not create this project. Try again.");
		expect(sheet).not.toHaveTextContent("request_id");
		await waitFor(() => expect(sheet).toHaveClass("modal-shake"));
	});

	it("submits single_repo imports without a blocking branch lookup", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn(async () => undefined);
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST.mockResolvedValueOnce({ data: projectValidation("/repo/project") });

		renderChooseFlow({ onCreateProject });
		await openSource(user, "Import an existing project");
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repo/project",
				asWorkspace: false,
				workerAgent: "codex",
				orchestratorAgent: "codex",
			}),
		);
		// The daemon resolves the base branch itself; the import must not
		// block on a branch lookup before submitting.
		expect(bridgeMocks.getRepositoryBranch).not.toHaveBeenCalled();
	});

	it("preserves the checked-out root branch when importing a workspace", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn(async () => undefined);
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		bridgeMocks.getRepositoryBranch.mockResolvedValue("main");
		bridgeMocks.scanImportFolder.mockResolvedValue({
			path: "/repo/project",
			repos: [{ ...okScan("/repo/project/app").repos[0], name: "app", relativePath: "app" }],
		});
		apiMocks.POST.mockResolvedValueOnce({
			data: {
				...projectValidation("/repo/project", {
					root: { isRepo: false, hasCommit: false, hasOrigin: false, needsGitInit: true },
					childRepos: [{
						repoPath: "/repo/project/app", isRepo: true, hasCommit: true, hasOrigin: true,
						isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [],
					}],
				}),
				importKind: "workspace",
			},
		});

		renderChooseFlow({ onCreateProject });
		await openSource(user, "Import a workspace folder");
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Submit agents" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repo/project",
				asWorkspace: true,
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
		let resolveInit!: (value: unknown) => void;
		let resolveCommit!: (value: unknown) => void;
		let resolveRemote!: (value: unknown) => void;
		apiMocks.POST
			.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: {
						isRepo: false,
						hasCommit: false,
						hasOrigin: false,
						needsGitInit: true,
						requiredActions: ["git_init", "git_commit", "create_remote_repository"],
					},
				}),
			})
			.mockReturnValueOnce(
				new Promise((resolve) => {
					resolveInit = resolve;
				}),
			)
			.mockReturnValueOnce(new Promise((resolve) => {
				resolveCommit = resolve;
			}))
			.mockReturnValueOnce(new Promise((resolve) => {
				resolveRemote = resolve;
			}));

		render(
			<CreateProjectFlow mode="choose" {...noop}>
				{({ choosePath }) => <button onClick={choosePath}>New project</button>}
			</CreateProjectFlow>,
		);

		await openSource(user, "Import an existing project");
		const ownerInput = await screen.findByLabelText("Owner");
		await user.click(ownerInput);
		await user.click(await screen.findByRole("option", { name: "acme" }));
			await waitFor(() => expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeEnabled());
			await user.click(screen.getByRole("button", { name: "Create repository and continue" }));

		expect(await screen.findByText("Running project setup. AO is preparing this repository now.")).toBeInTheDocument();
		expect(screen.getAllByText("In progress")).toHaveLength(1);
		expect(screen.getAllByText("Queued")).toHaveLength(2);
		expect(apiMocks.POST).toHaveBeenCalledTimes(2);

		resolveInit({
			data: {
				events: [
					{ repoPath: "/repo/project", action: "git_init", state: "success" },
				],
				validation: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: { isRepo: true, hasCommit: false, hasOrigin: false, requiredActions: ["git_commit", "create_remote_repository"] },
				}),
			},
		});
		await waitFor(() => expect(apiMocks.POST).toHaveBeenCalledTimes(3));
		expect(screen.getAllByText("Done")).toHaveLength(1);
		expect(screen.getAllByText("In progress")).toHaveLength(1);
		expect(screen.getAllByText("Queued")).toHaveLength(1);

		resolveCommit({
			data: {
				events: [{ repoPath: "/repo/project", action: "git_commit", state: "success" }],
				validation: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: { isRepo: true, hasCommit: true, hasOrigin: false, requiredActions: ["create_remote_repository"] },
				}),
			},
		});
		await waitFor(() => expect(apiMocks.POST).toHaveBeenCalledTimes(4));
		expect(screen.getAllByText("Done")).toHaveLength(2);
		expect(screen.getAllByText("In progress")).toHaveLength(1);

		resolveRemote({
			data: {
				events: [{ repoPath: "/repo/project", action: "create_remote_repository", state: "success" }],
				validation: projectValidation("/repo/project"),
			},
		});

		expect((await screen.findByTestId("agent-sheet"))).toHaveAttribute("data-path", "/repo/project");
	});

	it("shows a failed preparation step and allows retry", async () => {
		const user = userEvent.setup();
		bridgeMocks.chooseDirectory.mockResolvedValue("/repo/project");
		apiMocks.POST
			.mockResolvedValueOnce({
				data: projectValidation("/repo/project", {
					nextStep: "prepare_git",
					root: {
						isRepo: false,
						hasCommit: false,
						hasOrigin: false,
						requiredActions: ["git_init", "git_commit", "create_remote_repository"],
					},
				}),
			})
			.mockResolvedValueOnce({
				data: {
					events: [{ repoPath: "/repo/project", action: "git_init", state: "success" }],
					validation: projectValidation("/repo/project", {
						nextStep: "prepare_git",
						root: { isRepo: true, hasCommit: false, hasOrigin: false, requiredActions: ["git_commit", "create_remote_repository"] },
					}),
				},
			})
			.mockResolvedValueOnce({
				data: {
					events: [
						{ repoPath: "/repo/project", action: "git_commit", state: "running" },
						{ repoPath: "/repo/project", action: "git_commit", state: "error", error: "commit hook failed" },
					],
					validation: projectValidation("/repo/project", {
						nextStep: "prepare_git",
						root: {
							hasOrigin: false,
							requiredActions: ["git_commit", "create_remote_repository"],
						},
					}),
				},
			})
			.mockResolvedValueOnce({
				data: {
					events: [{ repoPath: "/repo/project", action: "git_commit", state: "success" }],
					validation: projectValidation("/repo/project", {
						nextStep: "prepare_git",
						root: { isRepo: true, hasCommit: true, hasOrigin: false, requiredActions: ["create_remote_repository"] },
					}),
				},
			})
			.mockResolvedValueOnce({
				data: {
					events: [{ repoPath: "/repo/project", action: "create_remote_repository", state: "success" }],
					validation: projectValidation("/repo/project"),
				},
			});

		renderChooseFlow();
		await openSource(user, "Import an existing project");
		const ownerInput = await screen.findByLabelText("Owner");
		await user.click(ownerInput);
		await user.click(await screen.findByRole("option", { name: "acme" }));
			await waitFor(() => expect(screen.getByRole("button", { name: "Create repository and continue" })).toBeEnabled());
			await user.click(screen.getByRole("button", { name: "Create repository and continue" }));

		await waitFor(() => expect(useUiStore.getState().globalToast?.body).toMatch(/failed while running Initial commit/i));
		await waitFor(() => expect(screen.getByRole("dialog", { name: "Prepare project" })).toHaveClass("modal-shake"));
		expect(screen.getAllByText("Done")).toHaveLength(1);
		expect(screen.getAllByText("Needs attention")).toHaveLength(1);
		expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
		expect(screen.queryByTestId("agent-sheet")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect((await screen.findByTestId("agent-sheet"))).toHaveAttribute("data-path", "/repo/project");
		expect(apiMocks.POST).toHaveBeenCalledTimes(5);
		expect(apiMocks.POST.mock.calls[3]?.[1]).toMatchObject({
			body: { approvedActions: ["git_commit", "create_remote_repository"], stepwise: true },
		});
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
