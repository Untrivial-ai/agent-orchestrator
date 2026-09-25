import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

const composer = source("./ChatComposer.tsx");

/** The body of one entry in the composer's stylesheet. */
function styleRule(name: string): string {
	const match = composer.match(new RegExp(`\\n\\t${name}: \\{([^}]*)\\}`));
	expect(match, `${name} rule`).not.toBeNull();
	return match?.[1] ?? "";
}

describe("chat composer pill", () => {
	// The row is one capsule: attach, field, dictation and the one filled control
	// that commits. A border plus a fill made it read as a box with buttons in it.
	it("draws a single borderless capsule", () => {
		const pill = styleRule("composer");
		expect(pill).toContain("borderRadius: COMPOSER_RADIUS");
		expect(composer).toContain("const COMPOSER_RADIUS = 28");
		expect(pill).not.toContain("borderWidth");
		expect(pill).not.toContain("borderColor");
		// No fill under the glass: an opaque pill under a material is the one
		// arrangement that turns it grey. Android, which has no material, keeps the
		// elevated fill.
		expect(pill).toContain('backgroundColor: composerGlassSupported ? "transparent" : t.bgElevated');
	});

	it("lays the glass behind the row, sized by the pill itself", () => {
		// The material is handed a radius and nothing else: the pill owns its height,
		// the host fills the pill and the material fills the host. Three attempts at
		// sizing it from something read at runtime — a layout measurement, the space
		// SwiftUI was proposed mid-animation, a content-height count — each ended with
		// the material a different size from the pill behind it, and all three are read
		// while the keyboard is still moving. Filling reads nothing.
		expect(composer).toContain("<ComposerGlass radius={COMPOSER_RADIUS} />");
		expect(composer).not.toContain("<ComposerGlass height=");
		expect(composer).not.toContain("onLayout");
		// The field grows from the native content size while glass fills the pill,
		// avoiding a separately measured glass height that could lag behind it.
		expect(composer).toContain("onContentSizeChange={(event) => {");
		expect(composer).toContain("style={[styles.input, { height: text ? fieldHeight : COMPOSER_FIELD_HEIGHT }]}");
		expect(styleRule("composer")).toContain("minHeight: COMPOSER_HEIGHT");
		expect(styleRule("composer")).toContain("maxHeight: COMPOSER_MAX_HEIGHT");
		expect(styleRule("composer")).toContain('alignItems: "flex-end"');
		expect(styleRule("input")).toContain("minHeight: COMPOSER_FIELD_HEIGHT");
		expect(styleRule("input")).toContain("maxHeight: COMPOSER_FIELD_MAX_HEIGHT");
		expect(composer).toContain("const [fieldHeight, setFieldHeight] = useState(COMPOSER_FIELD_HEIGHT)");
		expect(composer).toContain("const contentHeight = Math.ceil(event.nativeEvent.contentSize.height)");
		expect(composer).toContain("setFieldHeight(Math.max(COMPOSER_FIELD_HEIGHT, Math.min(COMPOSER_FIELD_MAX_HEIGHT, contentHeight)))");
		// `false`: the material must not take the touch. The field is inside this
		// pill, and an interactive material only passes taps that land on its own
		// content — typing would stop working.
		expect(source("./composer-glass.ios.tsx")).toContain("glassPanel(radius, undefined, false)");
		expect(source("./composer-glass.ios.tsx")).toContain("frame({ maxWidth: 2000, maxHeight: 2000 })");
	});

	it("returns an emptied multiline draft to the one-line height", () => {
		// A controlled TextInput can retain its last native content size after its
		// value is cleared; the empty placeholder must not inherit that height.
		expect(composer).toContain('setText("");\n\t\t\tsetFieldHeight(COMPOSER_FIELD_HEIGHT);');
		expect(composer).toContain('if (!text) {\n\t\t\t\t\t\t\tsetFieldHeight(COMPOSER_FIELD_HEIGHT);');
		expect(composer).toContain("height: text ? fieldHeight : COMPOSER_FIELD_HEIGHT");
	});

	// Two discs side by side have no hierarchy; the send button is the only shape
	// that commits, so dictation is a bare glyph here.
	it("keeps dictation a glyph and the send a filled circle", () => {
		expect(composer).toContain('<MicKey variant="plain"');
		expect(styleRule("send")).toContain("borderRadius: 22");
		expect(styleRule("send")).toContain("backgroundColor: t.accent");
	});

	it("opens the row with a plus rather than a paperclip", () => {
		expect(source("./ChatAttachmentMenu.tsx")).toContain('name="plus"');
	});

	// The dock used to swap its inset for the keyboard gap on a visibility flag,
	// and that flag turns over when the keyboard has finished hiding — so the
	// composer, and the model label above it, spent the whole closing animation one
	// inset too low and then jumped into place at the end.
	it("rides the keyboard's own progress instead of switching inset on a flag", () => {
		expect(composer).toContain("useReanimatedKeyboardAnimation()");
		expect(composer).toContain("(restingInset - KEYBOARD_DOCK_GAP) * keyboard.progress.value");
		expect(composer).toContain("{ paddingBottom: restingInset }");
		expect(composer).not.toContain("dockInset(");
	});
});

describe("composer meta row", () => {
	const control = source("./ChatTurnSettingsControl.tsx");

	// "GPT-5.6-Sol · Medium · Ask when unsure" is the longest thing in the row, and
	// two others can join it: the queued-message note and the context meter. The
	// label was drawn straight across both — the settings control is the one that
	// yields, and it is clipped so a label that fails to truncate cannot paint over
	// its neighbours.
	it("lets the settings label yield instead of the meter", () => {
		const meta = styleRule("metaRow");
		expect(meta).toContain("gap: space.sm");
		expect(styleRule("settingsSlot")).toContain("overflow: \"hidden\"");
		expect(styleRule("contextMeter")).toContain("flexShrink: 0");
		expect(styleRule("deliveryNote")).toContain("flexShrink: 0");
		expect(control).toContain('alignSelf: "stretch"');
		expect(control).toContain('overflow: "hidden"');
	});
});
