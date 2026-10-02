import type { ReactNode } from "react";
import { cn } from "../lib/utils";

/** A set of setup choices. Rows share one container with dividers and carry no
 *  surface of their own: DESIGN.md prefers a shared list over a field of
 *  individually rounded cards. */
export function SetupList({ children, className }: { children: ReactNode; className?: string }) {
	return <div className={cn("flex w-full flex-col divide-y divide-border/60", className)}>{children}</div>;
}

export function SetupRow({ icon, label, description, trailing, trailingAction, disabled, selected, static: isStatic, onClick, variant = "row" }: {
	icon: ReactNode;
	label: string;
	description?: string;
	trailing?: ReactNode;
	trailingAction?: { label: string; onClick: () => void; disabled?: boolean };
	disabled?: boolean;
	selected?: boolean;
	/** Render an informative status row without button or disabled styling. */
	static?: boolean;
	onClick?: () => void;
	/** `row` sits in a shared list with dividers and no surface of its own;
	 *  `card` is a bordered standalone surface, `ghost` is transparent until
	 *  interaction, and `action` adds a quiet fill for command-like rows. */
	variant?: "row" | "card" | "ghost" | "action";
}) {
	const isCard = variant === "card";
	const isGhost = variant === "ghost";
	const isAction = variant === "action";
	const rowClassName = cn(
		"flex w-full items-center text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50",
		isCard
			// The border is always there so selecting a row changes colour
			// instead of nudging the text sideways.
			? "gap-3 rounded-xl border border-border/60 bg-card px-4 py-3 transition-colors hover:bg-muted active:scale-[0.99]"
			: isGhost || isAction
				? cn("gap-3 rounded-xl px-4 py-3 hover:bg-muted active:scale-[0.99]", isAction ? "bg-foreground/[0.035]" : "bg-transparent")
			: "gap-3.5 px-1 py-3.5 transition-colors hover:bg-foreground/[0.04]",
		!isCard && selected && "bg-foreground/[0.03]",
		// The chosen row is marked by a bright edge rather than the accent
		// colour, so selection reads the same as the text around it.
		isCard && selected && "border-foreground/45 bg-accent-weak ring-1 ring-inset ring-foreground/20",
	);
	const content = (
		<>
			<span className={cn(
				"grid shrink-0 place-items-center text-muted-foreground",
				isCard ? "size-8 [&_svg]:size-4" : isGhost || isAction ? "size-8 [&_svg]:size-5" : "size-6 [&_svg]:size-5",
			)}>{icon}</span>
			<span className="min-w-0 flex-1">
				<span className="block text-sm font-medium leading-5 text-foreground">{label}</span>
				{description ? <span className="mt-0.5 block text-caption leading-snug text-muted-foreground">{description}</span> : null}
			</span>
			{trailing ? <span className="shrink-0 text-caption text-muted-foreground">{trailing}</span> : null}
		</>
	);
	if (trailingAction) {
		return (
			<div aria-label={label} className={rowClassName}>
				{content}
				<button
					type="button"
					disabled={trailingAction.disabled}
					onClick={trailingAction.onClick}
					className="shrink-0 rounded-md px-2 py-1 text-caption font-medium text-foreground hover:bg-foreground/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50"
				>
					{trailingAction.label}
				</button>
			</div>
		);
	}
	if (isStatic) {
		return <div aria-label={label} className={rowClassName} role="status">{content}</div>;
	}
	return (
		<button
			type="button"
			aria-label={label}
			aria-pressed={selected}
			disabled={disabled}
			onClick={onClick}
			className={rowClassName}
		>
			{content}
		</button>
	);
}
