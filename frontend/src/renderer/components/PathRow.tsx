import type { ReactNode } from "react";

export function PathRow({ action, ariaDescribedBy, ariaInvalid, ariaLabel, children, disabled, icon, id, onClick }: {
	action: ReactNode;
	ariaDescribedBy?: string;
	ariaInvalid?: boolean;
	ariaLabel?: string;
	children: ReactNode;
	disabled?: boolean;
	icon: ReactNode;
	id?: string;
	onClick: () => void;
}) {
	return (
		<button aria-describedby={ariaDescribedBy} aria-invalid={ariaInvalid} aria-label={ariaLabel} id={id} type="button" className="flex h-control-form w-full items-center overflow-hidden rounded-md border border-transparent bg-[var(--color-bg-import-card)] text-left text-[13px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50" disabled={disabled} onClick={onClick}>
			<span className="flex min-w-0 flex-1 items-center gap-3 px-3">
				{icon}
				<span className="truncate">{children}</span>
			</span>
			<span className="flex h-full shrink-0 items-center border-l border-border/60 px-4 text-foreground hover:bg-foreground/10">{action}</span>
		</button>
	);
}
