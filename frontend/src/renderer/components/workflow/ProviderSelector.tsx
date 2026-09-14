import { useTranslation } from "react-i18next";
import { useProviders, useProviderModels } from "../../hooks/useProviders";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Label } from "../ui/label";

type ProviderSelectorProps = {
	providerId: string;
	modelId: string;
	onProviderChange: (providerId: string) => void;
	onModelChange: (modelId: string) => void;
	disabled?: boolean;
};

export function ProviderSelector({
	providerId,
	modelId,
	onProviderChange,
	onModelChange,
	disabled,
}: ProviderSelectorProps) {
	const { t } = useTranslation();
	const providersQuery = useProviders();
	const modelsQuery = useProviderModels(providerId);

	const providers = (providersQuery.data ?? []).filter((p) => p.enabled);
	const models = (modelsQuery.data ?? []).filter((m) => m.enabled);

	const handleProviderChange = (value: string) => {
		if (value === "__none__") {
			onProviderChange("");
			onModelChange("");
		} else {
			onProviderChange(value);
			onModelChange("");
		}
	};

	const handleModelChange = (value: string) => {
		onModelChange(value === "__none__" ? "" : value);
	};

	return (
		<div className="flex flex-col gap-3" data-testid="provider-selector">
			<div className="flex flex-col gap-1.5">
				<Label>{t("workflow.agentRole.defaultProvider")}</Label>
				<Select value={providerId || "__none__"} onValueChange={handleProviderChange} disabled={disabled}>
					<SelectTrigger data-testid="provider-select">
						<SelectValue placeholder={t("workflow.agentRole.systemDefault")} />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="__none__">{t("workflow.agentRole.systemDefault")}</SelectItem>
						{providers.map((p) => (
							<SelectItem key={p.id} value={p.id}>
								{p.displayName}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>

			<div className="flex flex-col gap-1.5">
				<Label>{t("workflow.agentRole.defaultModel")}</Label>
				<Select
					value={modelId || "__none__"}
					onValueChange={handleModelChange}
					disabled={disabled || !providerId}
				>
					<SelectTrigger data-testid="model-select">
						<SelectValue placeholder={t("workflow.agentRole.systemDefault")} />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="__none__">{t("workflow.agentRole.systemDefault")}</SelectItem>
						{models.map((m) => (
							<SelectItem key={m.id} value={m.id}>
								{m.displayName || m.modelName}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>

			{providerId && !modelId && (
				<p className="text-xs text-destructive">{t("workflow.agentRole.pairRequired")}</p>
			)}
			{!providerId && modelId && (
				<p className="text-xs text-destructive">{t("workflow.agentRole.pairRequired")}</p>
			)}
		</div>
	);
}
