import { useTranslation } from "react-i18next";
import {
	ProjectSettingsSection,
	ProjectSettingsValueRow,
} from "@aoagents/product-ui";
import { useCloudProjectsQuery } from "../hooks/useWorkspaceQuery";
import { ProductExternalLink } from "./ProductExternalLink";
import type { ProjectSettingsSection as ProjectSettingsSectionId } from "./ProjectSettingsForm";

/**
 * Read-only project specification for a project hosted by the AO cloud control
 * plane. Every value below comes from its control-plane Project response; a
 * cloud project is never looked up in, or saved through, the local daemon.
 *
 * The Cloud API intentionally leaves `config` open-ended while its project
 * configuration evolves. These are the currently recognized project fields.
 * Unknown values stay owned by Cloud rather than being copied into a local
 * project configuration.
 */
export function CloudProjectSettingsForm({
	projectId,
	section = "general",
}: {
	projectId: string;
	section?: ProjectSettingsSectionId;
}) {
	const { t } = useTranslation();
	const query = useCloudProjectsQuery();
	const project = query.data?.find((item) => item.id === projectId);
	const notConfigured = t("settings.harness.notConfigured");

	if (query.isLoading) {
		return <p className="text-sm text-settings-muted">{t("settings.project.loading")}</p>;
	}
	if (!project) {
		return (
			<p className="text-sm text-error">
				{query.error instanceof Error ? query.error.message : t("settings.project.loadFailed")}
			</p>
		);
	}

	const config = project.config;
	const worker = nestedString(config, "worker", "agent");
	const orchestrator = nestedString(config, "orchestrator", "agent");
	const reviewer = firstReviewerHarness(config);
	const sessionPrefix = stringValue(config.sessionPrefix);
	const autoReview = booleanValue(config.autoReview);
	const intakeEnabled = nestedBoolean(config, "trackerIntake", "enabled");
	const intakeRepository = nestedString(config, "trackerIntake", "repo");
	const intakeAssignee = nestedString(config, "trackerIntake", "assignee");

	return (
		<div data-testid="cloud-project-settings-form">
			{section === "general" && (
				<ProjectSettingsSection title={t("settings.project.identity")} titleHidden grouped>
					<ProjectSettingsValueRow label={t("settings.project.name")} value={project.displayName} />
					<ProjectSettingsValueRow label={t("settings.project.id")} value={project.id} />
					<ProjectSettingsValueRow
						externalLink={ProductExternalLink}
						href={project.repositoryUrl}
						label={t("settings.project.repo")}
						value={project.repositoryUrl || "—"}
					/>
					<ProjectSettingsValueRow label={t("settings.project.defaultBranch")} value={project.defaultBranch} />
				</ProjectSettingsSection>
			)}

			{section === "agents" && (
				<>
					<ProjectSettingsSection title={t("settings.project.agents")} titleHidden grouped>
						<ProjectSettingsValueRow label={t("settings.project.defaultWorker")} value={worker ?? notConfigured} />
						<ProjectSettingsValueRow label={t("settings.project.defaultOrchestrator")} value={orchestrator ?? notConfigured} />
					</ProjectSettingsSection>
					<ProjectSettingsSection title={t("settings.project.reviewer")} grouped>
						<ProjectSettingsValueRow label={t("settings.project.defaultReviewer")} value={reviewer ?? notConfigured} />
					</ProjectSettingsSection>
				</>
			)}

			{section === "workflow" && (
				<ProjectSettingsSection title={t("settings.project.workflow")} titleHidden grouped>
					<ProjectSettingsValueRow label={t("settings.project.defaultBranch")} value={project.defaultBranch} />
					<ProjectSettingsValueRow label={t("settings.project.sessionPrefix")} value={sessionPrefix ?? notConfigured} />
					<ProjectSettingsValueRow
						label={t("settings.project.autoReview")}
						value={autoReview === undefined ? notConfigured : t(autoReview ? "settings.updates.enabled" : "settings.updates.disabled")}
					/>
				</ProjectSettingsSection>
			)}

			{section === "intake" && (
				<ProjectSettingsSection title={t("settings.project.trackerIntake")} titleHidden grouped>
					<ProjectSettingsValueRow
						label={t("settings.project.enableIssueIntake")}
						value={intakeEnabled === undefined ? notConfigured : t(intakeEnabled ? "settings.updates.enabled" : "settings.updates.disabled")}
					/>
					<ProjectSettingsValueRow label={t("settings.project.repository")} value={intakeRepository ?? notConfigured} />
					<ProjectSettingsValueRow label={t("settings.project.assignee")} value={intakeAssignee ?? notConfigured} />
				</ProjectSettingsSection>
			)}
		</div>
	);
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
	return value !== null && typeof value === "object" && !Array.isArray(value)
		? value as Record<string, unknown>
		: undefined;
}

function stringValue(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

function booleanValue(value: unknown): boolean | undefined {
	return typeof value === "boolean" ? value : undefined;
}

function nestedString(config: Record<string, unknown>, parent: string, key: string): string | undefined {
	return stringValue(recordValue(config[parent])?.[key]);
}

function nestedBoolean(config: Record<string, unknown>, parent: string, key: string): boolean | undefined {
	return booleanValue(recordValue(config[parent])?.[key]);
}

function firstReviewerHarness(config: Record<string, unknown>): string | undefined {
	const reviewers = config.reviewers;
	if (!Array.isArray(reviewers)) return undefined;
	return stringValue(recordValue(reviewers[0])?.harness);
}
