import { ChevronDown, MessageSquareQuote, X } from "lucide-react";

/** The expansion deliberately reveals the selection, not the entire turn. */
export function ExcerptSelectionChip({ selection, role, onRemove, removeDisabled }: {
	selection: string;
	role?: string;
	onRemove?: () => void;
	removeDisabled?: boolean;
}) {
	const preview = selection.length > 80 ? `${selection.slice(0, 80)}…` : selection;
	return (
		<div className="inline-flex min-w-0 max-w-full items-start gap-1 rounded-lg border border-border/60 bg-muted/40 px-2 py-1.5 text-xs">
			<details className="group/reference min-w-0 flex-1">
				<summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-sm text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden" title={selection} aria-label={`Referenced selection: ${selection}`}>
					<MessageSquareQuote aria-hidden="true" className="size-3.5 shrink-0" />
					<span className="shrink-0 text-[11px]">{role === "assistant" ? "Agent" : "You"}</span>
					<span className="min-w-0 max-w-[240px] truncate text-foreground/80">{preview}</span>
					<ChevronDown aria-hidden="true" className="ml-auto size-3 shrink-0 transition-transform group-open/reference:rotate-180" />
				</summary>
				<p className="mt-2 max-h-40 max-w-xs overflow-y-auto whitespace-pre-wrap border-t border-border/60 pt-2 text-xs leading-5 text-foreground/80 [overflow-wrap:anywhere]">{selection}</p>
			</details>
			{onRemove ? <button type="button" onClick={onRemove} disabled={removeDisabled} aria-label="Remove referenced message"
				className="flex size-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground/70 outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50">
				<X aria-hidden="true" className="size-3" />
			</button> : null}
		</div>
	);
}
