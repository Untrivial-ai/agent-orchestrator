import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "../NavRowHighlight";

/**
 * Variants with no fill at rest. These get the growing pill (the sidebar's
 * hover) instead of a flat background swap, so every transparent button in the
 * app grows its fill the same way. Filled variants and the underline-only link
 * variants are untouched.
 */
const HIGHLIGHT_VARIANTS = new Set(["ghost", "quiet"]);

// Buttons are font-normal (400) with 6px radius; blue is the live edge
// (primary). Footer variants match settings/modal action chrome tokens.
// See DESIGN.md → Spacing / Color.
const buttonVariants = cva(
	"group/button inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-md border border-transparent bg-clip-padding text-sm font-normal transition-[background-color,border-color,color,box-shadow,transform,opacity] duration-fast ease-out select-none focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring not-aria-[haspopup]:scale-press not-aria-[haspopup]:active:translate-y-px disabled:pointer-events-none disabled:opacity-50 motion-reduce:transition-none motion-reduce:active:scale-100 motion-reduce:active:translate-y-0 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-icon-base",
	{
		variants: {
			variant: {
				primary: "bg-primary text-primary-foreground hover:bg-primary/80",
				outline:
					"border-border bg-background text-foreground hover:bg-interactive-hover aria-expanded:bg-interactive-hover aria-expanded:text-foreground dark:bg-transparent dark:hover:bg-interactive-hover",
				secondary:
					"bg-secondary text-secondary-foreground hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_5%)]",
				// Neutral buttons share one hover: the translucent interactive wash,
				// so they read the same on a card, a popover and the sidebar. Filled
				// variants adjust their own fill instead and stay out of this axis.
				ghost: "text-foreground hover:bg-interactive-hover",
				quiet: "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
				danger: "text-destructive hover:border-destructive/50 hover:bg-destructive/18 hover:text-destructive",
				/**
				 * A control whose hover surface is owned by an ancestor — the sidebar
				 * rail and its growing row highlight. The Button contributes the shared
				 * focus ring, press and disabled contract; it must not paint a second
				 * hover background on top of the highlight that is already animating.
				 */
				rail: "text-muted-foreground hover:text-foreground data-[state=open]:text-foreground",
				// Text that acts: reads as a link, behaves as a button (name, focus,
				// press). Underline on hover so it is not mistaken for static copy.
				link: "text-markdown-link underline-offset-2 hover:underline",
				// The secondary text action: reads as chrome until pointed at.
				"link-quiet": "text-muted-foreground underline-offset-2 hover:text-foreground hover:underline",
				footer:
					"h-(--size-settings-action-height) rounded-(--radius-settings-action) border-[var(--color-border-settings-input)] bg-[var(--color-bg-settings-input)] px-3 text-[length:var(--font-size-md)] leading-5 text-[var(--color-text-settings-row)] hover:bg-[var(--color-bg-settings-input)] hover:opacity-90 active:not-aria-[haspopup]:scale-100 active:not-aria-[haspopup]:translate-y-0",
				"footer-primary":
					"h-(--size-settings-action-height) rounded-(--radius-settings-action) border-transparent bg-[var(--color-accent)] px-(--size-settings-footer-button-padding-x) text-[length:var(--font-size-md)] leading-5 text-[var(--color-settings-footer-button-primary-fg)] hover:bg-[var(--color-accent)] hover:opacity-90 active:not-aria-[haspopup]:scale-100 active:not-aria-[haspopup]:translate-y-0",
			},
			size: {
				default: "h-control-form px-3",
				sm: "h-control-md px-2.5 text-xs",
				icon: "size-control-form",
				"icon-sm": "size-control-md",
				"icon-xs": "size-control-xs",
				"icon-lg": "size-control-board",
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
	({ asChild = false, children, className, size, variant, ...props }, ref) => {
		const Comp = asChild ? Slot : "button";
		// Footer chrome sets its own height/padding; don't let the default size
		// token fight those modal action measurements.
		const resolvedSize = variant === "footer" || variant === "footer-primary" ? "none" : size;
		// `asChild` forwards to the caller's element, which must stay a single
		// child; that path keeps the plain hover fill.
		const highlight = !asChild && HIGHLIGHT_VARIANTS.has(variant ?? "");
		return (
			<Comp
				className={cn(buttonVariants({ variant, size: resolvedSize }), highlight && NAV_ROW_HIGHLIGHT_HOST_CLASS, className)}
				ref={ref}
				{...props}
			>
				{highlight ? <NavRowHighlight disabled={Boolean(props.disabled)} /> : null}
				{children}
			</Comp>
		);
	},
);

Button.displayName = "Button";

export { buttonVariants };
