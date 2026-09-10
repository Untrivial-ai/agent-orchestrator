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
 * When the selected model advertises no levels this renders nothing rather than
 * an empty or disabled control. An effort dropdown on a model that ignores
 * effort is a promise the agent will not keep.
 */
export function AgentEffortSelect({
	value,
	efforts,
	onChange,
	disabled,
	"aria-label": ariaLabel,
}: {
	value: string;
	efforts: string[] | undefined;
	onChange: (value: string) => void;
	disabled?: boolean;
	"aria-label"?: string;
}) {
	const { t } = useTranslation();
	if (!efforts || efforts.length === 0) return null;

	// The empty value is a real choice, not a placeholder: it leaves whatever
	// default the agent would pick on its own, which is what a user who has
	// never touched this setting already has.
	const options = [
		{ value: "", label: t("settings.models.agentDefaultEffort") },
		...efforts.map((effort) => ({ value: effort, label: effortLabel(effort) })),
	];

	return (
		<SettingsOptionMenu
			aria-label={ariaLabel ?? t("settings.models.effort")}
			value={value}
			options={options}
			onChange={onChange}
			disabled={disabled}
			triggerClassName="justify-end"
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
