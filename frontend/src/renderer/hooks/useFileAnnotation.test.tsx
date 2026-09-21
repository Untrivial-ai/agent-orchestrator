import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
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
});
