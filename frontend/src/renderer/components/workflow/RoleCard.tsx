import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { Switch } from "../ui/switch";
import { useSetAgentRoleEnabled } from "../../hooks/useWorkflowRoles";

type AgentRoleView = components["schemas"]["ControllersAgentRoleView"];

type RoleCardProps = {
	role: AgentRoleView;
	onEdit: () => void;
};

export function RoleCard({ role, onEdit }: RoleCardProps) {
	const { t } = useTranslation();
	const toggleEnabled = useSetAgentRoleEnabled(role.id);

	const hasProvider = !!(role.defaultProviderId && role.defaultProviderModelId);

	return (
		<Card data-testid={`role-card-${role.id}`}>
			<CardHeader>
				<div className="flex items-start justify-between gap-2">
					<div className="min-w-0 flex-1">
						<CardTitle className="line-clamp-1 text-sm">{role.displayName || role.name}</CardTitle>
						{role.displayName && role.displayName !== role.name && (
							<p className="mt-0.5 text-xs text-muted-foreground">{role.name}</p>
						)}
					</div>
					<Badge variant={role.enabled ? "success" : "neutral"}>
						{role.enabled ? t("status.role.enabled") : t("status.role.disabled")}
					</Badge>
				</div>
			</CardHeader>
			<CardContent className="flex flex-col gap-2">
				{role.description && (
					<p className="line-clamp-2 text-xs text-muted-foreground">{role.description}</p>
				)}
				<div className="text-xs text-muted-foreground">
					{hasProvider ? (
						<span>
							{role.defaultProviderId} / {role.defaultProviderModelId}
						</span>
					) : (
						<span>{t("workflow.agentRole.noDefault")}</span>
					)}
				</div>
				<div className="flex items-center justify-between pt-2">
					<div className="flex items-center gap-2">
						<Switch
							checked={role.enabled}
							onCheckedChange={(enabled) => toggleEnabled.mutate(enabled)}
							data-testid={`role-toggle-${role.id}`}
						/>
					</div>
					<Button variant="ghost" size="sm" onClick={onEdit} data-testid={`role-edit-${role.id}`}>
						{t("workflow.editRole")}
					</Button>
				</div>
			</CardContent>
		</Card>
	);
}
