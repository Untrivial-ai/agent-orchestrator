import { describe, expect, it } from "vitest";
import { DISPLAY_STATUSES, displayStatusLabelKeys, getDisplayStatusLabel } from "@aoagents/product-ui";
import type { components } from "../../api/schema";
import schema from "../../api/schema.ts?raw";

type WireDisplayStatus = components["schemas"]["ControllersSessionView"]["displayStatus"];
type LocalDisplayStatus = (typeof DISPLAY_STATUSES)[number];
const complete: [Exclude<WireDisplayStatus, LocalDisplayStatus>, Exclude<LocalDisplayStatus, WireDisplayStatus>] extends [never, never] ? true : false = true;

describe("display status contract", () => {
	it("covers the generated API enum and translates every phrase in every locale", () => {
		expect(complete).toBe(true);
		const locales = import.meta.glob("../i18n/*.json", { eager: true, import: "default" }) as Record<string, Record<string, string>>;
		expect(Object.keys(locales).length).toBeGreaterThan(0);
		for (const [locale, messages] of Object.entries(locales)) {
			for (const status of DISPLAY_STATUSES) {
				const key = displayStatusLabelKeys[status];
				expect(messages[key], `${locale}: ${key}`).toBeTruthy();
				expect(getDisplayStatusLabel(status, (key) => messages[key])).toBe(messages[key]);
			}
		}
	});

	it("matches the wire enum at runtime as well as during typecheck", () => {
		const body = schema.split("ControllersSessionView: {")[1]?.split("\n        };")[0];
		const enumLine = body?.match(/displayStatus: ([^;]+);/);
		expect(enumLine).not.toBeNull();
		const wire = enumLine![1].split(" | ").map((value) => JSON.parse(value) as string);
		expect([...DISPLAY_STATUSES].sort()).toEqual(wire.sort());
	});
});
