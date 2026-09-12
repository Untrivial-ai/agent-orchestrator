import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

// Buttons are font-normal (400) with 6px radius; blue is the live edge
// (primary). Footer variants match settings/modal action chrome tokens.
// See DESIGN.md → Spacing / Color.
const buttonVariants = cva(
	"group/button inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-md border border-transparent bg-clip-padding text-sm font-normal transition-[background-color,border-color,color,box-shadow,transform,opacity] duration-[100ms] ease-out outline-none select-none focus-visible:outline-none active:not-aria-[haspopup]:scale-[0.96] active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
	{
		variants: {
			variant: {
				primary: "bg-primary text-primary-foreground hover:bg-primary/80",
				outline:
					"border-border bg-background text-foreground hover:bg-muted aria-expanded:bg-muted aria-expanded:text-foreground dark:bg-transparent dark:hover:bg-input/30",
				secondary:
					"bg-secondary text-secondary-foreground hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_5%)]",
				ghost: "text-foreground hover:bg-muted dark:hover:bg-muted/50",
				footer:
					"h-(--size-settings-action-height) rounded-(--radius-settings-action) border-[var(--color-border-settings-input)] bg-[var(--color-bg-settings-input)] px-3 text-[length:var(--font-size-md)] leading-5 text-[var(--color-text-settings-row)] hover:bg-[var(--color-bg-settings-input)] hover:opacity-90 active:not-aria-[haspopup]:scale-100 active:not-aria-[haspopup]:translate-y-0 focus-visible:outline-none",
				"footer-primary":
					"h-(--size-settings-action-height) rounded-(--radius-settings-action) border-transparent bg-[var(--color-settings-accent)] px-(--size-settings-footer-button-padding-x) text-[length:var(--font-size-md)] leading-5 text-[var(--color-settings-footer-button-primary-fg)] hover:bg-[var(--color-settings-accent)] hover:opacity-90 active:not-aria-[haspopup]:scale-100 active:not-aria-[haspopup]:translate-y-0 focus-visible:outline-none",
			},
			size: {
				default: "h-control-form px-3",
				sm: "h-control-md px-2.5 text-xs",
				icon: "size-control-form",
				"icon-sm": "size-control-md",
				// Neutral size so footer variants own height/padding via tokens.
				none: "",
			},
		},
		defaultVariants: {
			variant: "primary",
			size: "default",
		},
	},
);

export interface ButtonProps
	extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
	asChild?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
	({ asChild = false, className, size, variant, ...props }, ref) => {
		const Comp = asChild ? Slot : "button";
		// Footer chrome sets its own height/padding; don't let the default size
		// token fight those modal action measurements.
		const resolvedSize = variant === "footer" || variant === "footer-primary" ? "none" : size;
		return <Comp className={cn(buttonVariants({ variant, size: resolvedSize, className }))} ref={ref} {...props} />;
	},
);

Button.displayName = "Button";

export { buttonVariants };
