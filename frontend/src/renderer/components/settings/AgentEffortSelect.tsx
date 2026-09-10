import { useTranslation } from "react-i18next";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

/**
 * Reasoning-effort picker for the selected model.
 *
 * The levels are not a fixed list and must never be hardcoded: they are
 * per-model and come from the provider's own catalog. Sonnet 4.5 and Haiku 4.5
 * accept no effort at all, Opus 4.6 omits `xhigh`, and the 5 family accepts all
 * five — so a static enum would be wrong for most of the catalog and would go
 * further out of date with every model release.
 *
 * The control renders only when there is something to choose. With no model
 * selected there is no list to offer, and on a model that accepts no effort a
 * dropdown would be a promise the agent will not keep — so both cases fall
 * back to the plain model picker rather than showing a dead control.
 *
 * The cost is that the row reflows when moving between models that do and do
 * not take effort. That is the accepted trade: a control that is present but
 * never usable is worse than one that appears when it applies.
 */
export function AgentEffortSelect({
	value,
	efforts,
	onChange,
	disabled,
	triggerClassName,
	"aria-label": ariaLabel,
}: {
	value: string;
	efforts: string[] | undefined;
	onChange: (value: string) => void;
	disabled?: boolean;
	triggerClassName?: string;
	"aria-label"?: string;
}) {
	const { t } = useTranslation();
	const available = efforts ?? [];
	// Nothing to choose: show the plain model picker instead of a dead control.
	if (available.length === 0) return null;

	// The empty value is a real choice, not a placeholder: it leaves whatever
	// default the agent would pick on its own, which is what a user who has
	// never touched this setting already has.
	const options = [
		{ value: "", label: t("settings.models.agentDefaultEffort") },
		...available.map((effort) => ({ value: effort, label: effortLabel(effort) })),
	];

	return (
		<SettingsOptionMenu
			aria-label={ariaLabel ?? t("settings.models.effort")}
			// A level stored against a previous model must never be displayed
			// beside one that cannot take it.
			value={available.includes(value) ? value : ""}
			options={options}
			onChange={onChange}
			disabled={disabled}
			triggerClassName={triggerClassName ?? "justify-end"}
		/>
	);
}

/**
 * effortLabel title-cases a level for display while leaving the value itself
 * untouched — the provider's spelling is what gets sent to `claude --effort`.
 *
 * `xhigh` is special-cased because "Xhigh" reads as a typo.
 */
export function effortLabel(effort: string): string {
	if (effort === "xhigh") return "Extra high";
	if (effort.length === 0) return effort;
	return effort.charAt(0).toUpperCase() + effort.slice(1);
}
