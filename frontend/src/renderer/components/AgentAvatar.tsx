import {
	AgentAvatar as ProductAgentAvatar,
	type AgentAvatarProps,
	type AgentLogoSources,
} from "@aoagents/product-ui";
import opencodeLogo from "../assets/agents/opencode.svg";

// Real brand logo keyed by the harness name AO stores on session.provider.
// Agents without an asset fall back to a lettered tile.
const LOGOS: AgentLogoSources = {
	opencode: opencodeLogo,
};

/**
 * Agent mark for board/task cards: the harness's real brand logo rendered bare —
 * no tile, border, or background — so each brand's own shape shows agents
 * carrying their own rounded background. Agents without an asset fall back to a
 * bare initial. Kept small so the title stays the hero.
 *
 * The provider is exposed as the accessible name (alt / aria-label), not just a
 * hover title, so surfaces that show the logo in place of visible agent text —
 * e.g. the archive cards — still name the agent for screen readers.
 */
export function AgentAvatar({ provider, className, decorative = false }: AgentAvatarProps) {
	return <ProductAgentAvatar className={className} decorative={decorative} logoSources={LOGOS} provider={provider} />;
}

export type { AgentAvatarProps };
