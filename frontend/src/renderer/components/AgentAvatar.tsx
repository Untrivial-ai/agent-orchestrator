import {
	AgentAvatar as ProductAgentAvatar,
	type AgentAvatarProps,
	type AgentLogoSources,
} from "@aoagents/product-ui";
import agyLogo from "../assets/agents/agy.png";
import aiderLogo from "../assets/agents/aider.png";
import ampLogo from "../assets/agents/amp.svg";
import auggieLogo from "../assets/agents/auggie.svg";
import autohandLogo from "../assets/agents/autohand.svg";
import clineLogo from "../assets/agents/cline.svg";
import claudeLogo from "../assets/agents/claude.svg";
import claudeCodeLogo from "../assets/agents/claude-code.svg";
import codexLogo from "../assets/agents/codex.svg";
import continueLogo from "../assets/agents/continue.png";
import copilotLogo from "../assets/agents/copilot.svg";
import crushLogo from "../assets/agents/crush.png";
import cursorLogo from "../assets/agents/cursor.svg";
import devinLogo from "../assets/agents/devin.png";
import droidLogo from "../assets/agents/droid.png";
import gooseLogo from "../assets/agents/goose.svg";
import grokLogo from "../assets/agents/grok.png";
import kilocodeLogo from "../assets/agents/kilocode.svg";
import kimiLogo from "../assets/agents/kimi.png";
import kimchiLogo from "../assets/agents/kimchi.svg";
import kiroLogo from "../assets/agents/kiro.png";
import museLogo from "../assets/agents/muse.png";
import ompLogo from "../assets/agents/omp.png";
import opencodeLogo from "../assets/agents/opencode.svg";
import piLogo from "../assets/agents/pi.png";
import primeAgentLogo from "../assets/agents/prime-agent.png";
import qwenLogo from "../assets/agents/qwen.png";
import vibeLogo from "../assets/agents/vibe.png";

// Real brand logos keyed by the harness name AO stores on session.provider.
// Agents without an asset fall back to a lettered tile (fake).
const LOGOS: AgentLogoSources = {
	codex: codexLogo,
	"claude-code": claudeCodeLogo,
	claude: claudeLogo,
	cursor: cursorLogo,
	opencode: opencodeLogo,
	copilot: copilotLogo,
	aider: aiderLogo,
	grok: grokLogo,
	droid: droidLogo,
	crush: crushLogo,
	qwen: qwenLogo,
	goose: gooseLogo,
	continue: continueLogo,
	devin: devinLogo,
	kimi: kimiLogo,
	muse: museLogo,
	omp: ompLogo,
	kiro: kiroLogo,
	kilocode: kilocodeLogo,
	vibe: vibeLogo,
	pi: piLogo,
	kimchi: kimchiLogo,
	"prime-agent": primeAgentLogo,
	amp: ampLogo,
	cline: clineLogo,
	agy: agyLogo,
	auggie: auggieLogo,
	autohand: autohandLogo,
};

/**
 * Agent mark for board/task cards: the harness's real brand logo rendered bare —
 * no tile, border, or background — so each brand's own shape shows (codex,
 * claude, cursor, … carry their own rounded backgrounds). Agents without an
 * asset fall back to a bare initial. Kept small so the title stays the hero.
 *
 * The provider is exposed as the accessible name (alt / aria-label), not just a
 * hover title, so surfaces that show the logo in place of visible agent text —
 * e.g. the archive cards — still name the agent for screen readers.
 */
export function AgentAvatar({ provider, className, decorative = false }: AgentAvatarProps) {
	return <ProductAgentAvatar className={className} decorative={decorative} logoSources={LOGOS} provider={provider} />;
}

export type { AgentAvatarProps };
