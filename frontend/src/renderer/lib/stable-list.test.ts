import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { sameContent, useStableList } from "./stable-list";

const keyOf = (entry: { id: string }) => entry.id;

describe("sameContent", () => {
	it("compares nested objects and arrays by value", () => {
		expect(sameContent({ a: 1, b: { c: [1, 2] } }, { a: 1, b: { c: [1, 2] } })).toBe(true);
		expect(sameContent({ a: 1, b: { c: [1, 2] } }, { a: 1, b: { c: [1, 3] } })).toBe(false);
	});

	it("does not treat a missing key as equal to an undefined one", () => {
		// `{}` and `{ detail: undefined }` render the same today, but an item that
		// gained a field must still count as changed or the row will not update.
		expect(sameContent({}, { detail: undefined })).toBe(false);
	});

	it("distinguishes arrays that differ only in length or order", () => {
		expect(sameContent([1, 2], [1, 2, 3])).toBe(false);
		expect(sameContent([1, 2], [2, 1])).toBe(false);
	});
});

describe("useStableList", () => {
	it("hands back the previous object for an entry that did not change", () => {
		const first = [{ id: "a", n: 1 }, { id: "b", n: 2 }];
		const { result, rerender } = renderHook(
			({ list }) => useStableList(list, keyOf, sameContent),
			{ initialProps: { list: first } },
		);
		const stable = result.current;

		// A refetch: same content, all-new objects, which is what JSON parsing gives.
		rerender({ list: [{ id: "a", n: 1 }, { id: "b", n: 2 }] });
		expect(result.current[0]).toBe(stable[0]);
		expect(result.current[1]).toBe(stable[1]);
	});

	it("keeps the list reference when a refetch changes no entries", () => {
		const { result, rerender } = renderHook(
			({ list }) => useStableList(list, keyOf, sameContent),
			{ initialProps: { list: [{ id: "a", n: 1 }, { id: "b", n: 2 }] } },
		);
		const stable = result.current;

		rerender({ list: [{ id: "a", n: 1 }, { id: "b", n: 2 }] });
		expect(result.current).toBe(stable);
	});

	it("adopts the new object for the one entry that changed", () => {
		const { result, rerender } = renderHook(
			({ list }) => useStableList(list, keyOf, sameContent),
			{ initialProps: { list: [{ id: "a", n: 1 }, { id: "b", n: 2 }] } },
		);
		const stable = result.current;

		const changed = { id: "b", n: 3 };
		rerender({ list: [{ id: "a", n: 1 }, changed] });
		expect(result.current[0]).toBe(stable[0]);
		expect(result.current[1]).toBe(changed);
	});

	it("keeps identity across a poll that also appends", () => {
		const { result, rerender } = renderHook(
			({ list }) => useStableList(list, keyOf, sameContent),
			{ initialProps: { list: [{ id: "a", n: 1 }] } },
		);
		const stable = result.current[0];

		rerender({ list: [{ id: "a", n: 1 }, { id: "b", n: 2 }] });
		expect(result.current[0]).toBe(stable);
		expect(result.current).toHaveLength(2);
	});

	it("forgets entries that left the list", () => {
		const { result, rerender } = renderHook(
			({ list }) => useStableList(list, keyOf, sameContent),
			{ initialProps: { list: [{ id: "a", n: 1 }] } },
		);
		const original = result.current[0];

		rerender({ list: [] });
		rerender({ list: [{ id: "a", n: 1 }] });
		// Held forever, a long-lived surface would accumulate every item it ever saw.
		expect(result.current[0]).not.toBe(original);
	});
});
