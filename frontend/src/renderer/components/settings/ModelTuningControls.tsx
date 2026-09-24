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
	const concreteModel = model.toLowerCase() === "default" ? "" : model;
	const selected =
		(concreteModel ? models?.find((item) => item.id === concreteModel) : undefined) ??
		(concreteModel === "" ? models?.find((item) => item.isDefault && item.id.toLowerCase() !== "default") : undefined);
	const capabilitiesKnown = models !== undefined;
	const invalidEffort = Boolean(effort && capabilitiesKnown && !selected?.efforts?.includes(effort));

	useEffect(() => {
		if (previousModel.current === model) return;
		if (!capabilitiesKnown) return;
		previousModel.current = model;
		if (effort && !selected?.efforts?.includes(effort)) onEffortReset("");
	}, [capabilitiesKnown, effort, model, onEffortReset, selected]);

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
	const effortOptions = selected.efforts?.filter((value) => value && value.toLowerCase() !== "default") ?? [];
	const explicitEffort = effort.toLowerCase() === "default" ? "" : effort;
	const effectiveEffort = explicitEffort || (effortOptions.includes(selected.defaultEffort ?? "") ? selected.defaultEffort : "") || "";
	const effortControl = effortOptions.length ? (
		<SettingsOptionMenu
			aria-label={`${prefix}${t("settings.models.effort")}`}
			value={effectiveEffort}
			placeholder={t("settings.models.effortNotReported")}
			disabled={disabled}
			options={effortOptions.map((value) => ({ value, label: value }))}
			onChange={onEffortChange}
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
