import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { useAgentRoles } from "../../hooks/useWorkflowRoles";
import { RoleCard } from "./RoleCard";
import { AgentRoleDialog } from "./AgentRoleDialog";
import type { components } from "../../../api/schema";
import { ProjectWorkspaceNav } from "../ProjectWorkspaceNav";

type AgentRoleView = components["schemas"]["ControllersAgentRoleView"];

type AgentRolePageProps = {
	projectId: string;
};

export function AgentRolePage({ projectId }: AgentRolePageProps) {
	const { t } = useTranslation();
	const rolesQuery = useAgentRoles();
	const [dialogMode, setDialogMode] = useState<"create" | { role: AgentRoleView } | null>(null);

	const roles = rolesQuery.data ?? [];

	return (
		<div className="flex h-full flex-col">
			<ProjectWorkspaceNav active="workflow" projectId={projectId} />
			<div className="flex flex-col gap-4 p-4">
				<div className="flex items-center justify-between">
					<h1 className="text-lg font-semibold">{t("workflow.agentRoles")}</h1>
					<Button onClick={() => setDialogMode("create")} size="sm" data-testid="create-role-button">
						<Plus className="mr-1 h-4 w-4" />
						{t("workflow.createRole")}
					</Button>
				</div>

				{rolesQuery.isLoading ? (
					<div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
						{Array.from({ length: 3 }).map((_, i) => (
							<Skeleton key={i} className="h-44 rounded-lg" />
						))}
					</div>
				) : rolesQuery.isError ? (
					<div className="flex flex-col items-center gap-2 py-12 text-muted-foreground">
						<p>{t("workflow.error.rolesLoadFailed")}</p>
						<Button variant="outline" size="sm" onClick={() => void rolesQuery.refetch()}>
							{t("common.retry")}
						</Button>
					</div>
				) : roles.length === 0 ? (
					<div className="flex flex-col items-center gap-2 py-12 text-muted-foreground">
						<p className="text-sm">{t("workflow.empty.noRoles")}</p>
						<Button variant="outline" size="sm" onClick={() => setDialogMode("create")}>
							<Plus className="mr-1 h-4 w-4" />
							{t("workflow.createRole")}
						</Button>
					</div>
				) : (
					<div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
						{roles.map((role) => (
							<RoleCard
								key={role.id}
								role={role}
								onEdit={() => setDialogMode({ role })}
							/>
						))}
					</div>
				)}

				{dialogMode !== null && (
					<AgentRoleDialog
						open
						onOpenChange={() => setDialogMode(null)}
						role={dialogMode === "create" ? undefined : dialogMode.role}
					/>
				)}
			</div>
		</div>
	);
}
