import { useTranslation } from "react-i18next";
import { ImportSessionList } from "../ImportSessionList";
import { SettingsSection } from "./SettingsSection";

// ImportSessionsSection is the settings home for bringing existing agent
// conversations into AO. It appears twice: in global settings, listing every
// conversation on the machine, and in a project's settings scoped to that
// project, so someone arriving from Claude Code or Codex can migrate either
// everything at once or one project at a time.
export function ImportSessionsSection({
	titleHidden,
	projectId,
	onImported,
}: {
	titleHidden?: boolean;
	projectId?: string;
	onImported?: (sessionId: string, projectId?: string) => void;
}) {
	const { t } = useTranslation();
	return (
		<SettingsSection
			title={t("settings.importSessions")}
			sectionId="import-sessions"
			titleHidden={titleHidden}
		>
			<div className="rounded-md bg-[var(--color-bg-settings-row)] px-4 py-4">
				<p className="mb-3 text-caption leading-4 text-muted-foreground">
					{projectId ? t("importSession.descriptionProject") : t("importSession.description")}
				</p>
				<ImportSessionList projectId={projectId} onImported={onImported} />
			</div>
		</SettingsSection>
	);
}
