import type { ReactNode } from "react";
import { cn } from "../lib/utils";

/** A set of setup choices. Rows share one container with dividers and carry no
 *  surface of their own: DESIGN.md prefers a shared list over a field of
 *  individually rounded cards. */
export function SetupList({ children, className }: { children: ReactNode; className?: string }) {
	return <div className={cn("flex w-full flex-col divide-y divide-border/60", className)}>{children}</div>;
}

export function SetupRow({ icon, label, description, trailing, disabled, selected, onClick, variant = "row" }: {
	icon: ReactNode;
	label: string;
	description?: string;
	trailing?: ReactNode;
	disabled?: boolean;
	selected?: boolean;
	onClick?: () => void;
	/** `row` sits in a shared list with dividers and no surface of its own;
	 *  `card` is the standalone rounded surface the project step uses. */
	variant?: "row" | "card";
}) {
	const isCard = variant === "card";
	return (
		<button
			type="button"
			aria-label={label}
			aria-pressed={selected}
			disabled={disabled}
			onClick={onClick}
			className={cn(
				"flex w-full items-center text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50",
				isCard
					// The border is always there so selecting a row changes colour
					// instead of nudging the text sideways.
					? "gap-3 rounded-lg border border-transparent bg-card px-4 py-3 hover:bg-muted active:scale-[0.99]"
					: "gap-3.5 px-1 py-3.5 hover:bg-foreground/[0.04]",
				!isCard && selected && "bg-foreground/[0.03]",
				// The chosen row is marked by a bright edge rather than the accent
				// colour, so selection reads the same as the text around it.
				isCard && selected && "border-foreground/45 bg-accent-weak ring-1 ring-inset ring-foreground/20",
			)}
		>
			<span className={cn("grid shrink-0 place-items-center text-muted-foreground", isCard ? "size-8 [&_svg]:size-4" : "size-6 [&_svg]:size-5")}>{icon}</span>
			<span className="min-w-0 flex-1">
				<span className="block text-sm font-medium leading-5 text-foreground">{label}</span>
				{description ? <span className="mt-0.5 block text-caption leading-snug text-muted-foreground">{description}</span> : null}
			</span>
			{trailing ? <span className="shrink-0 text-caption text-muted-foreground">{trailing}</span> : null}
		</button>
	);
}
