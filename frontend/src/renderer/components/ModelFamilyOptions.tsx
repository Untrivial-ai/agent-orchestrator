import { groupModelFamilies, modelVersionLabel, type FamilyModel } from "@aoagents/product-ui";
import { OptionMenuItem, OptionMenuSub, OptionMenuSubContent, OptionMenuSubTrigger } from "./ui/option-menu";

/** Shared by task creation, native chat models, and ACP model controls. */
export function ModelFamilyOptions({ models, value, onSelect, disabled = false }: {
	models: FamilyModel[]; value?: string; onSelect(id: string): void; disabled?: boolean;
}) {
	const row = (model: FamilyModel) => (
		<OptionMenuItem key={model.id} active={model.id === value} disabled={disabled} onSelect={() => onSelect(model.id)} className="text-xs">
			<span className="flex min-w-0 flex-col">
				<span>{modelVersionLabel(model)}</span>
				{model.label !== model.id && !/^(?:claude-)?(?:opus|fable|sonnet|haiku|mythos)(?:\[[^\]]+\])?$/i.test(model.id) ? <span aria-hidden="true" className="text-[10px] text-muted-foreground">{model.id}</span> : null}
			</span>
		</OptionMenuItem>
	);
	return <>{groupModelFamilies(models).map((family) => family.nested ? (
		<OptionMenuSub key={family.key}>
			<OptionMenuSubTrigger label={family.label} disabled={disabled} />
			<OptionMenuSubContent scrollable className="chat-settings-menu text-xs"><div className="max-h-72 overflow-y-auto overscroll-contain">{family.models.map(row)}</div></OptionMenuSubContent>
		</OptionMenuSub>
	) : family.models.map(row))}</>;
}
