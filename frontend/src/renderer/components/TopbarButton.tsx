import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";

/** Transparent topbar controls get the same growing pill as the sidebar rows. */
const HIGHLIGHT_VARIANTS = new Set(["secondary", "icon", "kill", "killIcon"]);

const topbarButtonVariants = cva(
	"topbar-control topbar-control--disabled-affordance inline-flex items-center transition-[transform,filter,background-color,color,border-color] duration-fast ease-out scale-press focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring",
	{
		variants: {
			variant: {
				secondary:
					"topbar-control--secondary h-control-lg gap-1.5 rounded-md px-3.5 text-sm font-semibold leading-none text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
				primary:
					"topbar-control--primary h-control-lg gap-1.5 rounded-md bg-accent-strong px-3.5 text-sm font-semibold leading-none text-accent-foreground hover:brightness-110 active:brightness-95",
				accent:
					"topbar-control--accent h-control-lg gap-1.5 rounded-md border border-border px-3.5 text-sm font-semibold leading-none bg-raised text-muted-foreground hover:bg-surface hover:text-foreground",
				feature:
					"topbar-control--feature h-control-lg gap-1.5 rounded-md border px-3 text-control font-semibold leading-none",
				icon:
					"topbar-control--icon grid size-control-md place-items-center rounded-md text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
				kill: "h-control-lg gap-1.5 rounded-md border border-transparent bg-transparent px-3.5 text-sm font-semibold leading-none text-destructive/80 hover:border-destructive/50 hover:bg-destructive/18 hover:text-destructive",
				killIcon:
					"topbar-control--icon topbar-control--danger-icon grid size-control-md place-items-center rounded-md text-destructive/80 hover:bg-destructive/18 hover:text-destructive",
				killConfirm:
					"h-control-lg gap-1.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 text-control font-semibold leading-none text-destructive hover:bg-destructive/18",
				killCancel:
					"h-control-lg rounded-md px-2.5 text-control font-semibold leading-none text-muted-foreground hover:text-foreground",
				// The workspace handoff is one split control. Its joined edges and
				// token-backed widths live beside the shared topbar rules in styles.css.
				splitMain:
					"topbar-control--split-main topbar-control--labeled h-control-lg gap-1.5 rounded-l-md border border-r-0 border-border text-sm font-semibold leading-none bg-raised text-muted-foreground hover:bg-surface hover:text-foreground",
				splitTrigger:
					"topbar-control--split-trigger grid h-control-lg place-items-center rounded-r-md border border-border bg-raised text-muted-foreground hover:bg-surface hover:text-foreground data-[state=open]:bg-surface data-[state=open]:text-foreground",
			},
		},
		defaultVariants: { variant: "primary" },
	},
);

export function TopbarButton({
	className,
	children,
	variant,
	type = "button",
	...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof topbarButtonVariants>) {
	const highlight = HIGHLIGHT_VARIANTS.has(variant ?? "");
	return (
		<button
			className={cn(topbarButtonVariants({ variant }), highlight && NAV_ROW_HIGHLIGHT_HOST_CLASS, className)}
			type={type}
			{...props}
		>
			{highlight ? <NavRowHighlight disabled={Boolean(props.disabled)} /> : null}
			{children}
		</button>
	);
}

export function TopbarActionError({ className, ...props }: React.HTMLAttributes<HTMLSpanElement>) {
	return <span className={cn("text-caption text-destructive", className)} role="alert" {...props} />;
}

export const topbarHeaderClass =
	"center-panel-titlebar flex h-toolbar shrink-0 items-center gap-3 border-b border-border-strong pr-4 z-chrome";

export const topbarProjectLabelClass =
	"text-brand font-semibold tracking-tight leading-none text-foreground whitespace-nowrap";
