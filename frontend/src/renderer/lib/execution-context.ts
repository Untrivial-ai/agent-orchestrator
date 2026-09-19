import type { TFunction } from "i18next";
import type { ExecutionContextLabels } from "@aoagents/product-ui";
import type { components } from "../../api/schema";

type Project = components["schemas"]["Project"];

export function projectRepositories(project: Project | undefined): string[] {
	if (!project) return [];
	return [...new Set([project.repo, ...(project.workspaceRepos ?? []).map((repo) => repo.repo)].filter(Boolean))];
}

export function executionContextLabels(t: TFunction): ExecutionContextLabels {
	return {
		active: t("executionContext.active"),
		baseBranch: t("settings.project.defaultBranch"),
		branch: t("inspector.branch"),
		configured: t("executionContext.configured"),
		executionContext: t("executionContext.title"),
		loading: t("executionContext.loading"),
		orchestrator: t("settings.models.orchestratorRole"),
		path: t("settings.project.path"),
		repository: t("settings.project.repository"),
		worker: t("settings.models.workerRole"),
	};
}
