import { describe, expect, it } from "vitest";
import { nativePermissionMenu, permissionMenuLabel, providerPermissionMenu } from "./permission-menu";

const labels = ["Auto", "Manual", "Accept Edits", "Don't Ask", "Bypass Permissions"];
describe("shared AO permission menu", () => {
	it("exposes the same five actions and explicit Codex values", () => {
		const items = nativePermissionMenu("codex");
		expect(items.slice(0, 5).map((item) => item.label)).toEqual(labels);
		expect(items.slice(0, 5).map((item) => item.value)).toEqual(["auto", "manual", "accept-edits", "dont-ask", "bypass-permissions"]);
		expect(permissionMenuLabel(items, "default")).toBe("Provider configuration");
	});
	it("disables unadvertised policies instead of assuming native support", () => {
		expect(nativePermissionMenu("unknown").slice(0, 5).every((item) => item.value === undefined)).toBe(true);
		const items = providerPermissionMenu([{ value: "default", name: "Interactive" }, { value: "bypassPermissions", name: "Unrestricted" }], "claude-code");
		expect(items.map((item) => item.label)).toEqual(labels);
		expect(items.map((item) => item.value)).toEqual([undefined, "default", undefined, undefined, "bypassPermissions"]);
	});
	it("maps native IDs while preserving custom provider choices and values", () => {
		const items = providerPermissionMenu([
			{ value: "acceptEdits", name: "Edit freely" }, { value: "dontAsk", name: "No prompts" },
			{ value: "custom-review", name: "Auto", description: "Custom restricted review" },
		]);
		expect(items[0].value).toBeUndefined();
		expect(items[2].value).toBe("acceptEdits");
		expect(items[3].value).toBe("dontAsk");
		expect(items[5]).toMatchObject({ value: "custom-review", label: "Auto", hint: "Custom restricted review" });
	});
	it.each(["kimchi", "omp"])("maps %s native yolo only when advertised", (harness) => {
		const items = providerPermissionMenu([{ value: "yolo", name: "YOLO" }, { value: "custom", name: "Custom" }], harness);
		expect(items[4]).toMatchObject({ label: "Bypass Permissions", value: "yolo" });
		expect(permissionMenuLabel(items, "yolo")).toBe("Bypass Permissions");
		expect(items[5]).toMatchObject({ value: "custom", label: "Custom" });
		expect(providerPermissionMenu([], harness)[4].value).toBeUndefined();
	});
	it("does not infer a manual posture from an unknown provider default", () => {
		const items = providerPermissionMenu([{ value: "default", name: "Configured policy" }, { value: "yolo", name: "Custom YOLO" }], "unknown");
		expect(items[1].value).toBeUndefined();
		expect(items[4].value).toBeUndefined();
		expect(permissionMenuLabel(items, "default")).toBe("Configured policy");
		expect(items[5]).toMatchObject({ value: "default", label: "Configured policy" });
		expect(items[6]).toMatchObject({ value: "yolo", label: "Custom YOLO" });
	});

});
