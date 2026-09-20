import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useFileAnnotation } from "./useFileAnnotation";

describe("useFileAnnotation", () => {
	it("closes feedback when the same trigger is clicked again", () => {
		const { result } = renderHook(() => useFileAnnotation("sess-1"));
		const target = {
			path: "src/App.tsx",
			side: "new" as const,
			line: 12,
			scope: "unstaged",
			surface: "focused" as const,
		};

		act(() => result.current.begin(target));
		expect(result.current.target).toEqual(target);

		act(() => result.current.begin({ ...target }));
		expect(result.current.target).toBeNull();
	});

	it("submits feedback through the provided Cloud message sender", async () => {
		const sendMessage = vi.fn().mockResolvedValue(undefined);
		const { result } = renderHook(() => useFileAnnotation("cloud-session", sendMessage));

		act(() => result.current.begin({ path: "src/App.tsx", side: "file", scope: "combined", surface: "focused" }));
		act(() => result.current.setDraft("Please simplify this file."));
		await act(async () => result.current.submit());

		expect(sendMessage).toHaveBeenCalledOnce();
		expect(sendMessage).toHaveBeenCalledWith(expect.stringContaining("Please simplify this file."));
		expect(result.current.status).toBe("sent");
	});
});
