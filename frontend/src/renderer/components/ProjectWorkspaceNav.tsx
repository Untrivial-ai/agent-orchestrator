import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";

type Tab = "sessions" | "workflow";

type ProjectWorkspaceNavProps = {
	active: Tab;
	projectId: string;
};

export function ProjectWorkspaceNav({ active, projectId }: ProjectWorkspaceNavProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();

	const tabs: { key: Tab; label: string; to: string }[] = [
		{ key: "sessions", label: t("workspace.tab.sessions"), to: "/projects/$projectId" },
		{ key: "workflow", label: t("workspace.tab.workflow"), to: "/projects/$projectId/workflow" },
	];

	return (
		<nav className="flex items-center gap-1 border-b px-4" data-testid="project-workspace-nav">
			{tabs.map((tab) => (
				<button
					key={tab.key}
					type="button"
					onClick={() => {
						if (tab.key !== active) {
							void navigate({ to: tab.to, params: { projectId } });
						}
					}}
					className={cn(
						"relative px-3 py-2 text-sm font-medium transition-colors hover:text-foreground",
						active === tab.key
							? "text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-primary"
							: "text-muted-foreground",
					)}
					data-testid={`workspace-tab-${tab.key}`}
				>
					{tab.label}
				</button>
			))}
		</nav>
	);
}
