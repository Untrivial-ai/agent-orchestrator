import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useShell } from "../lib/shell-context";
import { CreateProjectFlow } from "./CreateProjectFlow";
import { GitHubOnboardingNotice } from "./GitHubOnboardingNotice";
import { WelcomePanel } from "./WelcomePanel";

// Board empty states: first-launch welcome (`BoardWelcome`) and project board
// with no worker sessions yet (`ProjectBoardEmpty`).
export function BoardWelcome() {
	const { cloneProject, createProject, initializeProjectRepository } = useShell();
	return (
		<WelcomePanel>
			<div
				className="flex h-full min-h-0 items-center justify-center overflow-y-auto px-6 py-8"
				data-testid="board-welcome"
			>
				<div className="flex w-full flex-col items-center gap-3">
					<CreateProjectFlow
						embedded
						mode="choose"
						onCloneProject={cloneProject}
						onCreateProject={createProject}
						onInitializeProject={initializeProjectRepository}
					/>
					<GitHubOnboardingNotice />
				</div>
			</div>
		</WelcomePanel>
	);
}

// The center owns the prominent actions while the header keeps quiet copies.
export function ProjectBoardEmpty({ actions }: { actions: ReactNode }) {
	const { t } = useTranslation();
	return (
		<div className="flex h-full min-h-0 items-center justify-center overflow-y-auto" data-testid="project-board-empty">
			<div className="flex w-full max-w-preview-content flex-col items-center pb-empty-offset-y text-center">
				<h2 className="text-subtitle font-semibold tracking-tight text-foreground">{t("board.empty.title")}</h2>
				<p className="mt-2 text-md-sm leading-relaxed text-muted-foreground">{t("board.empty.body")}</p>
				{actions}
			</div>
		</div>
	);
}
