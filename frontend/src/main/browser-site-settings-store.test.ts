import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { expect, it } from "vitest";
import { BrowserSiteSettingsStore } from "./browser-site-settings-store";

it("persists settings per profile and origin, keeps temporary grants in memory, and resets only the selected site", async () => {
	const directory = await mkdtemp(path.join(os.tmpdir(), "ao-site-settings-"));
	try {
		const profile = "11111111-1111-4111-8111-111111111111";
		const store = new BrowserSiteSettingsStore(directory);
		await store.initialize();
		await Promise.all([
			store.set(profile, "https://example.com", "camera", "allow"),
			store.set(profile, "https://example.com", "microphone", "ask"),
			store.set(profile, "https://other.example", "camera", "allow"),
			store.set("temporary", "https://example.com", "location", "allow"),
		]);
		expect(store.get(profile, "https://example.com")).toMatchObject({ camera: "allow", microphone: "ask" });
		expect(store.get("another-profile", "https://example.com").camera).toBe("ask");
		expect(store.get(profile, "http://example.com").camera).toBe("ask");
		expect(await readFile(path.join(directory, "browser-site-settings.json"), "utf8")).not.toContain("temporary");
		const restored = new BrowserSiteSettingsStore(directory);
		await restored.initialize();
		expect(restored.get(profile, "https://example.com").camera).toBe("allow");
		expect(restored.get("temporary", "https://example.com").location).toBe("ask");
		await restored.reset(profile, "https://example.com");
		expect(restored.get(profile, "https://example.com").camera).toBe("ask");
		expect(restored.get(profile, "https://other.example").camera).toBe("allow");
	} finally {
		// directory is created above with a fixed prefix under the system temp directory.
		await rm(directory, { recursive: true, force: true });
	}
});
