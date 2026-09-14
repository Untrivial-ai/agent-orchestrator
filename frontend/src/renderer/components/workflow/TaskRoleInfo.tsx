import { useTranslation } from "react-i18next";
import { Badge } from "../ui/badge";
import { useAgentRole } from "../../hooks/useWorkflowRoles";
import { useProviders } from "../../hooks/useProviders";

type TaskRoleInfoProps = {
	agentRoleId?: string | null;
	providerId?: string | null;
	providerModelId?: string | null;
};

const sourceKeyMap: Record<string, string> = {
	taskOverride: "workflow.task.providerSource.taskOverride",
	roleDefault: "workflow.task.providerSource.roleDefault",
	systemDefault: "workflow.task.providerSource.systemDefault",
};

function useProviderSource(
	agentRoleId: string | undefined | null,
	providerId: string | undefined | null,
	roleDefaultProviderId: string | undefined | null,
): string | null {
	if (providerId) return "taskOverride";
	if (agentRoleId && roleDefaultProviderId) return "roleDefault";
	if (agentRoleId && !roleDefaultProviderId) return "systemDefault";
	if (!agentRoleId && !providerId) return "systemDefault";
	return null;
}

export function TaskRoleInfo({ agentRoleId, providerId, providerModelId }: TaskRoleInfoProps) {
	const { t } = useTranslation();
	const roleQuery = useAgentRole(agentRoleId || null);
	const providersQuery = useProviders();
	const role = roleQuery.data;
	const providers = providersQuery.data ?? [];

	const provider = providerId ? providers.find((p) => p.id === providerId) : null;
	const source = useProviderSource(agentRoleId, providerId, role?.defaultProviderId);

	const hasAnyData = agentRoleId || providerId || providerModelId;
	if (!hasAnyData) return null;

	return (
		<div className="flex flex-col gap-2" data-testid="task-role-info">
			<p className="text-xs font-medium text-muted-foreground">{t("workflow.task.roleInfo")}</p>

			{agentRoleId && (
				<div className="flex items-center gap-2 text-sm">
					<span className="text-muted-foreground">{t("workflow.task.role")}:</span>
					<span data-testid="role-info-name">{role?.displayName || role?.name || agentRoleId}</span>
					{role && !role.enabled && (
						<Badge variant="error" data-testid="role-info-disabled-badge">
							{t("status.role.disabled")}
						</Badge>
					)}
				</div>
			)}

			{(providerId || source) && (
				<div className="flex items-center gap-2 text-sm">
					<span className="text-muted-foreground">{t("workflow.task.provider")}:</span>
					<span data-testid="role-info-provider">{provider?.displayName || providerId || t("workflow.agentRole.systemDefault")}</span>
					{source && sourceKeyMap[source] && (
						<Badge variant="outline" data-testid="provider-source-badge">
							{t(sourceKeyMap[source] as "workflow.task.providerSource.taskOverride")}
						</Badge>
					)}
				</div>
			)}

			{providerModelId && (
				<div className="flex items-center gap-2 text-sm">
					<span className="text-muted-foreground">{t("workflow.task.model")}:</span>
					<span data-testid="role-info-model">{providerModelId}</span>
				</div>
			)}

			{role?.systemPrompt && (
				<div className="flex flex-col gap-1 text-sm">
					<span className="text-muted-foreground">{t("workflow.agentRole.systemPrompt")}:</span>
					<p
						className="text-xs text-muted-foreground line-clamp-3 whitespace-pre-wrap"
						data-testid="role-info-prompt"
					>
						{role.systemPrompt}
					</p>
				</div>
			)}
		</div>
	);
}
