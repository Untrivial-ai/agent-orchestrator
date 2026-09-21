import { MaterialCommunityIcons } from "@expo/vector-icons";
import { NotificationTypeIcon } from "./notification-type-icon";
import { WorkersIcon } from "./workers-icon";
import type { SidebarDestination, SidebarDestinationId } from "./sidebar-navigation";

/**
 * Outline when idle, filled when selected — the same pairing iOS gets from SF
 * Symbols, which Feather cannot express: it has no open folder and no filled
 * variants, so Projects and Workers both had to settle for a generic glyph.
 *
 * Workers was an activity pulse, which reads as monitoring rather than agents.
 * Desktop has no equivalent nav item to copy, so this follows the product's own
 * language: a worker is an agent doing the work.
 */
const glyphs: Record<Exclude<SidebarDestinationId, "prs" | "agents">, { idle: keyof typeof MaterialCommunityIcons.glyphMap; active: keyof typeof MaterialCommunityIcons.glyphMap }> = {
	projects: { idle: "folder-outline", active: "folder-open" },
	settings: { idle: "cog-outline", active: "cog" },
};

export function SidebarDestinationIcon({
	destination,
	color,
	active = false,
}: {
	destination: SidebarDestination;
	color: string;
	active?: boolean;
}) {
	// Pull requests draw the renderer's own lucide glyph, shared with the
	// notifications and the PR page.
	if (destination.id === "agents") return <WorkersIcon size={20} color={color} />;
	if (destination.id === "prs") return <NotificationTypeIcon icon="git-pull-request-arrow" size={20} color={color} />;
	const glyph = glyphs[destination.id];
	// No RNHostView here: this path is Android's, and hosting a vector-icon glyph
	// inside a Compose view renders nothing at all.
	return <MaterialCommunityIcons name={active ? glyph.active : glyph.idle} size={21} color={color} />;
}
