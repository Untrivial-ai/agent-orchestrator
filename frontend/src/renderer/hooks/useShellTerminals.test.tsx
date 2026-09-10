import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { patchMock } = vi.hoisted(() => ({ patchMock: vi.fn() }));
const shellStoreMock = vi.hoisted(() => ({
	load: vi.fn(async () => undefined),
	setPreference: vi.fn(async () => undefined),
	preference: { kind: "auto" as string, path: undefined as string | undefined },
}));
const { deleteMock, postMock, isWindowsMock } = vi.hoisted(() => ({
	deleteMock: vi.fn(),
	postMock: vi.fn(),
	isWindowsMock: vi.fn(() => false),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { DELETE: deleteMock, PATCH: patchMock, POST: postMock },
	apiErrorCode: (error: unknown) =>
		typeof error === "object" && error !== null && "code" in error ? (error as { code?: string }).code : undefined,
	hasTrustedApiBaseUrl: () => true,
}));

vi.mock("../lib/platform", () => ({ isWindowsPlatform: isWindowsMock }));
vi.mock("../stores/terminal-shell-store", () => ({
	terminalShellRequestValue: (preference: { kind: string; path?: string }) =>
		preference.kind === "custom" ? preference.path?.trim() || "auto" : preference.kind,
		useTerminalShellStore: { getState: () => shellStoreMock },
}));

import {
	type ShellTerminal,
	shellTerminalsQueryKey,
	useCloseShellTerminal,
	useOpenShellTerminal,
	useRenameShellTerminal,
} from "./useShellTerminals";

const shells: ShellTerminal[] = [
	{
		createdAt: "2026-08-27T00:00:00Z",
		handleId: "ptyhost-v1:shellterm-one",
		title: "one",
		workingDir: "/tmp",
	},
	{
		createdAt: "2026-08-27T00:01:00Z",
		handleId: "ptyhost-v1:shellterm-two",
		title: "two",
		workingDir: "/tmp",
	},
];

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

function queryClientWithShells() {
	const queryClient = new QueryClient({
		defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
	});
	queryClient.setQueryData(shellTerminalsQueryKey, shells);
	return queryClient;
}

beforeEach(() => {
	deleteMock.mockReset();
	patchMock.mockReset();
	postMock.mockReset();
	isWindowsMock.mockReturnValue(false);
	shellStoreMock.load.mockClear();
	shellStoreMock.setPreference.mockClear();
	shellStoreMock.preference = { kind: "auto", path: undefined };
});

describe("useOpenShellTerminal", () => {
	it("publishes the returned shell immediately without waiting for a list refetch", async () => {
		const shell = shells[0];
		postMock.mockResolvedValue({
			data: {
				shellTerminal: {
					createdAt: shell.createdAt,
					handleId: shell.handleId,
					title: shell.title,
					workingDir: shell.workingDir,
				},
			},
		});
		const queryClient = new QueryClient({
			defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
		});
		queryClient.setQueryData(shellTerminalsQueryKey, []);
		const { result } = renderHook(() => useOpenShellTerminal(), { wrapper: wrapper(queryClient) });

		await act(async () => result.current.mutateAsync({}));

		expect(queryClient.getQueryData(shellTerminalsQueryKey)).toEqual([shell]);
	});

	it("sends the saved Windows shell preference to the daemon", async () => {
		isWindowsMock.mockReturnValue(true);
		shellStoreMock.preference = { kind: "git-bash", path: undefined };
		postMock.mockResolvedValue({ data: { shellTerminal: { ...shells[0] } } });
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		const { result } = renderHook(() => useOpenShellTerminal(), { wrapper: wrapper(queryClient) });

		await act(async () => result.current.mutateAsync({ projectId: "project-1" }));

		expect(shellStoreMock.load).toHaveBeenCalledOnce();
		expect(postMock).toHaveBeenCalledWith("/api/v1/shell-terminals", {
			body: { projectId: "project-1", shell: "git-bash" },
		});
	});

	it("lets an explicit shell override the saved preference", async () => {
		isWindowsMock.mockReturnValue(true);
		shellStoreMock.preference = { kind: "git-bash", path: undefined };
		postMock.mockResolvedValue({ data: { shellTerminal: { ...shells[0] } } });
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		const { result } = renderHook(() => useOpenShellTerminal(), { wrapper: wrapper(queryClient) });

		await act(async () => result.current.mutateAsync({ shell: "C:\\Tools\\bash.exe" }));

		expect(postMock).toHaveBeenCalledWith("/api/v1/shell-terminals", {
			body: { shell: "C:\\Tools\\bash.exe" },
		});
	});

	it("omits the shell field outside Windows", async () => {
		postMock.mockResolvedValue({ data: { shellTerminal: { ...shells[0] } } });
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		const { result } = renderHook(() => useOpenShellTerminal(), { wrapper: wrapper(queryClient) });

		await act(async () => result.current.mutateAsync({}));

		expect(postMock).toHaveBeenCalledWith("/api/v1/shell-terminals", { body: {} });
		expect(shellStoreMock.load).not.toHaveBeenCalled();
	});

	it("normalizes an unavailable configured shell back to Automatic", async () => {
		isWindowsMock.mockReturnValue(true);
		shellStoreMock.preference = { kind: "custom", path: "C:\\missing\\shell.exe" };
		postMock.mockResolvedValue({ error: { code: "SHELL_TERMINAL_SHELL_UNAVAILABLE" } });
		const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		const { result } = renderHook(() => useOpenShellTerminal(), { wrapper: wrapper(queryClient) });

		await expect(act(async () => result.current.mutateAsync({}))).rejects.toEqual({
			code: "SHELL_TERMINAL_SHELL_UNAVAILABLE",
		});
		await waitFor(() => expect(shellStoreMock.setPreference).toHaveBeenCalledWith({ kind: "auto" }));
	});
});

describe("useRenameShellTerminal", () => {
	it("updates the visible tab title before the daemon responds", async () => {
		let finishRename!: (result: { data: { shellTerminal: ShellTerminal } }) => void;
		patchMock.mockReturnValue(new Promise((resolve) => (finishRename = resolve)));
		const queryClient = queryClientWithShells();
		const { result } = renderHook(() => useRenameShellTerminal(), { wrapper: wrapper(queryClient) });

		act(() => result.current.mutate({ handleId: shells[0].handleId, title: "server — zsh" }));

		await waitFor(() =>
			expect(queryClient.getQueryData<ShellTerminal[]>(shellTerminalsQueryKey)?.[0]?.title).toBe("server — zsh"),
		);
		act(() => finishRename({ data: { shellTerminal: { ...shells[0], title: "server — zsh" } } }));
		await waitFor(() => expect(result.current.isPending).toBe(false));
	});
});

describe("useCloseShellTerminal", () => {
	it("removes the terminal tab before an in-flight list request finishes cancelling", async () => {
		let finishCancel!: () => void;
		let finishDelete!: (result: { error?: unknown }) => void;
		deleteMock.mockReturnValue(new Promise((resolve) => (finishDelete = resolve)));
		const queryClient = queryClientWithShells();
		vi.spyOn(queryClient, "cancelQueries").mockReturnValue(
			new Promise<void>((resolve) => {
				finishCancel = resolve;
			}),
		);
		const { result } = renderHook(() => useCloseShellTerminal(), { wrapper: wrapper(queryClient) });

		act(() => result.current.mutate(shells[0].handleId));

		await waitFor(() => expect(queryClient.getQueryData(shellTerminalsQueryKey)).toEqual([shells[1]]));
		expect(deleteMock).not.toHaveBeenCalled();
		expect(result.current.isPending).toBe(true);

		act(() => finishCancel());
		await waitFor(() => expect(deleteMock).toHaveBeenCalled());
		act(() => finishDelete({}));
		await waitFor(() => expect(result.current.isPending).toBe(false));
	});

	it("restores an optimistically removed tab when a live PTY fails to close", async () => {
		let finishDelete!: (result: { error: unknown }) => void;
		deleteMock.mockReturnValue(new Promise((resolve) => (finishDelete = resolve)));
		const queryClient = queryClientWithShells();
		const { result } = renderHook(() => useCloseShellTerminal(), { wrapper: wrapper(queryClient) });

		act(() => result.current.mutate(shells[0].handleId));
		await waitFor(() => expect(queryClient.getQueryData(shellTerminalsQueryKey)).toEqual([shells[1]]));

		act(() => finishDelete({ error: { code: "SHELL_TERMINAL_CLOSE_FAILED" } }));
		await waitFor(() => expect(queryClient.getQueryData(shellTerminalsQueryKey)).toEqual(shells));
	});

	it("does not restore a stale tab when the daemon reports that its PTY is already gone", async () => {
		deleteMock.mockResolvedValue({ error: { code: "SHELL_TERMINAL_NOT_FOUND" } });
		const queryClient = queryClientWithShells();
		const { result } = renderHook(() => useCloseShellTerminal(), { wrapper: wrapper(queryClient) });

		await expect(result.current.mutateAsync(shells[0].handleId)).resolves.toBeUndefined();
		expect(queryClient.getQueryData(shellTerminalsQueryKey)).toEqual([shells[1]]);
	});
});
