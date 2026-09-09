import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { useSessionLinkNavigation } from "./use-session-link-navigation";

const mocks = vi.hoisted(() => ({ navigate: vi.fn(), workspace: vi.fn() }));
vi.mock("./navigate-to-session", () => ({ useNavigateToSession: () => mocks.navigate }));
vi.mock("../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: () => mocks.workspace() }));

describe("useSessionLinkNavigation", () => {
	beforeEach(() => {
		mocks.navigate.mockReset();
		useUiStore.setState({ sessionLinkError: null, sessionLinkNotices: [], sessionLinkNoticeSequence: 0 });
		mocks.workspace.mockReturnValue({
			isSuccess: true,
			data: [
				{ id: "other-project", sessions: [{ id: "other-session", isTerminated: false }] },
				{ id: "project", sessions: [{ id: "session", title: "renamed", isTerminated: false }, { id: "terminated", isTerminated: true }] },
			],
		});
	});

	it("selects the exact cross-project session by stable ID", () => {
		const { result } = renderHook(() => useSessionLinkNavigation());
		act(() => expect(result.current("ao://sessions/other-project/other-session")).toBe(true));
		expect(mocks.navigate).toHaveBeenCalledWith("other-project", "other-session");
		expect(useUiStore.getState().sessionLinkError).toBeNull();
	});

	it("shows feedback instead of navigating to a terminated session", () => {
		const { result } = renderHook(() => useSessionLinkNavigation());
		act(() => expect(result.current("ao://sessions/project/terminated")).toBe(false));
		act(() => expect(result.current("ao://sessions/project/terminated")).toBe(false));
		expect(mocks.navigate).not.toHaveBeenCalled();
		expect(useUiStore.getState().sessionLinkNotices.map((notice) => notice.message)).toEqual([
			"Session terminated is terminated",
			"Session terminated is terminated",
		]);
	});

	it.each([
		["ao://sessions/project/missing", "missing or is not accessible"],
		["ao://sessions/project/session/kill", "malformed or unsupported"],
	])("rejects %s with actionable feedback", (url, message) => {
		const { result } = renderHook(() => useSessionLinkNavigation());
		act(() => expect(result.current(url)).toBe(false));
		expect(mocks.navigate).not.toHaveBeenCalled();
		expect(useUiStore.getState().sessionLinkError).toContain(message);
	});

	it("does not navigate when the workspace cannot be verified", () => {
		mocks.workspace.mockReturnValue({ isSuccess: false, data: undefined });
		const { result } = renderHook(() => useSessionLinkNavigation());
		act(() => expect(result.current("ao://sessions/project/session")).toBe(false));
		expect(useUiStore.getState().sessionLinkError).toContain("daemon connection");
	});
});
