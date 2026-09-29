import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const spawn = readFileSync(fileURLToPath(new URL("../app/spawn.tsx", import.meta.url)), "utf8");
const voiceInput = readFileSync(fileURLToPath(new URL("./voice/useVoiceInput.ts", import.meta.url)), "utf8");

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

	it("keeps iOS live dictation feedback with the keyboard-sticky controls", () => {
		expect(spawn).toContain('Platform.OS === "android" ? voiceFeedback : null');
		expect(spawn).toContain('<KeyboardStickyView offset={{ closed: 0, opened: 0 }}>\n\t\t\t\t{Platform.OS === "ios" ? voiceFeedback : null}');
		expect(spawn.indexOf('Platform.OS === "ios" ? voiceFeedback : null')).toBeLessThan(spawn.indexOf('<SpawnComposerControls'));
	});

	it("keeps Start task disabled while a stopped dictation waits for its final result", () => {
		// stop() is asynchronous: the hook stays in recording until onFinal runs.
		const finish = voiceInput.slice(voiceInput.indexOf("const finish = useCallback"), voiceInput.indexOf("const pressIn = useCallback"));
		expect(finish).toContain('if (current !== "recording") return;');
		expect(finish).toContain("device.stop();");
		expect(finish.slice(finish.indexOf('if (current !== "recording") return;'))).not.toContain('setPhase("idle")');
		expect(spawn).toContain('disabled={!projectId || !harness || busy || modelLoading || loading || listening || voice.state === "transcribing"}');
	});
});
