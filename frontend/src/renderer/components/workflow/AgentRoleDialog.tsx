import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { useCreateAgentRole, useUpdateAgentRole } from "../../hooks/useWorkflowRoles";
import { ProviderSelector } from "./ProviderSelector";
import type { components } from "../../../api/schema";

type AgentRoleView = components["schemas"]["ControllersAgentRoleView"];

type AgentRoleDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	role?: AgentRoleView;
};

export function AgentRoleDialog({ open, onOpenChange, role }: AgentRoleDialogProps) {
	const { t } = useTranslation();
	const isEdit = !!role;
	const createRole = useCreateAgentRole();
	const updateRole = useUpdateAgentRole(role?.id ?? "");

	const [name, setName] = useState("");
	const [displayName, setDisplayName] = useState("");
	const [description, setDescription] = useState("");
	const [systemPrompt, setSystemPrompt] = useState("");
	const [providerId, setProviderId] = useState("");
	const [modelId, setModelId] = useState("");

	useEffect(() => {
		if (role) {
			setName(role.name);
			setDisplayName(role.displayName ?? "");
			setDescription(role.description ?? "");
			setSystemPrompt(role.systemPrompt ?? "");
			setProviderId(role.defaultProviderId ?? "");
			setModelId(role.defaultProviderModelId ?? "");
		} else {
			setName("");
			setDisplayName("");
			setDescription("");
			setSystemPrompt("");
			setProviderId("");
			setModelId("");
		}
	}, [role]);

	const resetForm = () => {
		setName("");
		setDisplayName("");
		setDescription("");
		setSystemPrompt("");
		setProviderId("");
		setModelId("");
	};

	const isValidPair = (providerId === "" && modelId === "") || (providerId !== "" && modelId !== "");

	const handleSubmit = () => {
		if (!isEdit && !name.trim()) return;
		if (!isValidPair) return;

		if (isEdit) {
			updateRole.mutate(
				{
					displayName: displayName || null,
					description: description || null,
					systemPrompt: systemPrompt || null,
					defaultProviderId: providerId || null,
					defaultProviderModelId: modelId || null,
				},
				{
					onSuccess: () => {
						resetForm();
						onOpenChange(false);
					},
				},
			);
		} else {
			createRole.mutate(
				{
					name: name.trim(),
					displayName: displayName.trim() || undefined,
					description: description.trim() || undefined,
					systemPrompt: systemPrompt || undefined,
					defaultProviderId: providerId || undefined,
					defaultProviderModelId: modelId || undefined,
				},
				{
					onSuccess: () => {
						resetForm();
						onOpenChange(false);
					},
				},
			);
		}
	};

	const isPending = isEdit ? updateRole.isPending : createRole.isPending;
	const mutationError = isEdit ? updateRole.error : createRole.error;

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) resetForm();
				onOpenChange(nextOpen);
			}}
		>
			<DialogContent data-testid="agent-role-dialog" className="max-w-lg">
				<DialogHeader>
					<DialogTitle>{isEdit ? t("workflow.editRole") : t("workflow.createRole")}</DialogTitle>
				</DialogHeader>
				<div className="flex flex-col gap-3 max-h-[60vh] overflow-y-auto">
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="role-name">{t("workflow.agentRole.name")} *</Label>
						<Input
							id="role-name"
							value={name}
							onChange={(e) => setName(e.target.value)}
							disabled={isEdit}
							placeholder={t("workflow.agentRole.namePlaceholder")}
							autoFocus={!isEdit}
							data-testid="role-name-input"
						/>
						{!isEdit && name && !/^[a-zA-Z0-9_-]+$/.test(name) && (
							<p className="text-xs text-destructive">{t("workflow.agentRole.nameInvalid")}</p>
						)}
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="role-display-name">{t("workflow.agentRole.displayName")}</Label>
						<Input
							id="role-display-name"
							value={displayName}
							onChange={(e) => setDisplayName(e.target.value)}
							placeholder={t("workflow.agentRole.displayNamePlaceholder")}
							data-testid="role-display-name-input"
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="role-description">{t("workflow.agentRole.description")}</Label>
						<Input
							id="role-description"
							value={description}
							onChange={(e) => setDescription(e.target.value)}
							placeholder={t("workflow.agentRole.descriptionPlaceholder")}
							data-testid="role-description-input"
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="role-prompt">{t("workflow.agentRole.systemPrompt")}</Label>
						<textarea
							id="role-prompt"
							value={systemPrompt}
							onChange={(e) => setSystemPrompt(e.target.value)}
							rows={6}
							className="flex min-h-[120px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
							placeholder={t("workflow.agentRole.systemPromptPlaceholder")}
							data-testid="role-prompt-input"
						/>
					</div>
					<ProviderSelector
						providerId={providerId}
						modelId={modelId}
						onProviderChange={setProviderId}
						onModelChange={setModelId}
					/>
				</div>
				{mutationError && (
					<p className="text-sm text-destructive" data-testid="role-dialog-error">
						{mutationError.message}
					</p>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						{t("workflow.agentRole.cancel")}
					</Button>
					<Button
						onClick={handleSubmit}
						disabled={(!isEdit && !name.trim()) || !isValidPair || isPending}
						data-testid="role-dialog-submit"
					>
						{isPending ? t("common.creating") : t("workflow.agentRole.save")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
