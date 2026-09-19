import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { SettingsOptionMenu } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";

type Model = components["schemas"]["AgentModelInfo"];

export type ModelTuningControlsProps = {
	models?: Model[];
	model: string;
	effort: string;
	onEffortChange: (value: string) => void;
	onEffortReset?: (value: string) => void;
	onValidityChange?: (valid: boolean) => void;
	variant: "settings" | "composer";
	roleLabel?: string;
	disabled?: boolean;
};

export function useModelTuning(props: Omit<ModelTuningControlsProps, "variant" | "disabled">) {
	const {
		models,
		model,
		effort,
		onEffortChange,
		onEffortReset = onEffortChange,
		onValidityChange,
	} = props;
	const previousModel = useRef(model);
	const previousValidity = useRef<boolean | undefined>(undefined);
	const selected =
		models?.find((item) => item.id === model) ??
		(model === "" ? models?.find((item) => item.isDefault) : undefined);
	// Only a catalog model with known efforts can prove an effort unsupported.
	// Custom ids and empty catalogs are unverifiable, not invalid.
	const invalidEffort = Boolean(effort && selected && !selected.efforts?.includes(effort));

	useEffect(() => {
		if (previousModel.current === model) return;
		if (!selected) return;
		previousModel.current = model;
		if (effort && !selected.efforts?.includes(effort)) onEffortReset("");
	}, [effort, model, onEffortReset, selected]);

	useEffect(() => {
		const valid = !invalidEffort;
		if (previousValidity.current === valid) return;
		previousValidity.current = valid;
		onValidityChange?.(valid);
	}, [invalidEffort, onValidityChange]);
	return { selected, invalidEffort };
}

export function ModelTuningControls(props: ModelTuningControlsProps) {
	const { t } = useTranslation();
	const { effort, onEffortChange, variant, roleLabel, disabled } = props;
	const { selected, invalidEffort } = useModelTuning(props);
	const prefix = roleLabel ? `${roleLabel} ` : "";
	const warning = invalidEffort
		? t("settings.models.unsupportedTuning", { role: roleLabel ? `${roleLabel} ` : "" })
		: null;
	if (!selected) {
		return warning && variant === "settings" ? (
			<p role="alert" className="px-1 text-xs leading-row text-warning">{warning}</p>
		) : null;
	}
	const effortControl = selected.efforts?.length ? (
		<SettingsOptionMenu
			aria-label={`${prefix}${t("settings.models.effort")}`}
			value={effort || "__default__"}
			disabled={disabled}
			options={[
				{ value: "__default__", label: t("settings.models.providerDefault") },
				...selected.efforts.map((value) => ({ value, label: value })),
			]}
			onChange={(value) => onEffortChange(value === "__default__" ? "" : value)}
			triggerClassName={variant === "composer" ? "composer-chip composer-toolbar-option" : "justify-end"}
		/>
	) : null;
	if (!effortControl) return null;
	if (variant === "composer") {
		return effortControl;
	}
	return (
		<>
			{effortControl ? <SettingsRow label={`${prefix}${t("settings.models.effort")}`}>{effortControl}</SettingsRow> : null}
			{warning ? <p role="alert" className="px-1 text-xs leading-row text-warning">{warning}</p> : null}
		</>
	);
}
