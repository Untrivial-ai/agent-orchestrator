import { logoKey } from "./harnessLogo";

// The `require` half of the logo registry, split from harnessLogo.ts because Node
// cannot require a PNG and that module has to stay unit-testable. Metro resolves
// these at build time, so the map cannot be built from a template string.
//
// Keys must match LOGO_KEYS in harnessLogo.ts — harnessLogo.test.ts checks both
// against the real asset directory so a rename cannot silently drop a mark.
const LOGOS: Record<string, number> = {
	agy: require("../assets/agents/agy.png"),
	aider: require("../assets/agents/aider.png"),
	amp: require("../assets/agents/amp.png"),
	auggie: require("../assets/agents/auggie.png"),
	autohand: require("../assets/agents/autohand.png"),
	kimchi: require("../assets/agents/kimchi.png"),
	"claude-code": require("../assets/agents/claude-code.png"),
	cline: require("../assets/agents/cline.png"),
	codex: require("../assets/agents/codex.png"),
	continue: require("../assets/agents/continue.png"),
	copilot: require("../assets/agents/copilot.png"),
	crush: require("../assets/agents/crush.png"),
	cursor: require("../assets/agents/cursor.png"),
	devin: require("../assets/agents/devin.png"),
	droid: require("../assets/agents/droid.png"),
	goose: require("../assets/agents/goose.png"),
	grok: require("../assets/agents/grok.png"),
	kilocode: require("../assets/agents/kilocode.png"),
	kimi: require("../assets/agents/kimi.png"),
	kiro: require("../assets/agents/kiro.png"),
	muse: require("../assets/agents/muse.png"),
	opencode: require("../assets/agents/opencode.png"),
	pi: require("../assets/agents/pi.png"),
	qwen: require("../assets/agents/qwen.png"),
	vibe: require("../assets/agents/vibe.png"),
};

/** The image source for a harness, or null when there is no mark for it. */
export function logoFor(harness?: string | null): number | null {
	return LOGOS[logoKey(harness)] ?? null;
}
