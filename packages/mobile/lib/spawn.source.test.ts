import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const spawn = readFileSync(fileURLToPath(new URL("../app/spawn.tsx", import.meta.url)), "utf8");

describe("spawn composer", () => {
	it("reflows attachments and messages above keyboard-lifted controls on iOS", () => {
		expect(spawn).toContain('height={Platform.OS === "ios" ? promptRoom : undefined}');
		expect(spawn).toContain('Platform.OS === "ios" && keyboardHeight > 0 ? (');
		expect(spawn).toContain('height: keyboardHeight, marginTop: -space.sm');
		expect(spawn.indexOf('style={styles.messages}')).toBeLessThan(spawn.indexOf('height: keyboardHeight, marginTop: -space.sm'));
		expect(spawn.indexOf('height: keyboardHeight, marginTop: -space.sm')).toBeLessThan(spawn.indexOf('<KeyboardStickyView'));
		expect(spawn).toContain('promptHostFill: { height: undefined, flex: 1, minHeight: 0 }');
	});

	it("shows a prompt-specific error for the daemon's 16 KiB rejection", () => {
		expect(spawn).toContain('e instanceof ApiError && e.code === "PROMPT_TOO_LONG"');
		expect(spawn).toContain('Task prompt is too long. Keep it to 16 KiB or fewer');
	});
});
