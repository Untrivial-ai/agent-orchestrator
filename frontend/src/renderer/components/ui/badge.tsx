// Mono pill badges. Color is rare and meaningful (DESIGN.md → Color).
import * as React from "react";
import { cn } from "../../lib/utils";

type BadgeVariant = "neutral" | "outline" | "accent" | "success" | "warning" | "error";

export function Badge({
	className,
	variant = "neutral",
	...props
}: React.HTMLAttributes<HTMLSpanElement> & { variant?: BadgeVariant }) {
	return (
		<span
			data-slot="badge"
			className={cn(
				"inline-flex h-5 shrink-0 items-center justify-center gap-1 overflow-hidden whitespace-nowrap rounded-full border border-transparent px-2 py-0.5 text-xs font-medium transition-[background-color,border-color,color,box-shadow] focus-visible:outline-none [&>svg]:pointer-events-none [&>svg]:size-3",
				variant === "neutral" && "bg-secondary text-secondary-foreground",
				variant === "outline" && "border-border text-foreground hover:bg-muted",
				variant === "accent" && "bg-primary text-primary-foreground",
				variant === "success" && "border-success/40 text-success",
				variant === "warning" && "border-warning/40 text-warning",
				variant === "error" && "bg-destructive/10 text-destructive",
				className,
			)}
			{...props}
		/>
	);
}
